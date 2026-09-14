// reads bounded, downsampled telemetry series from TimescaleDB hypertables.
package postgres

import (
	"context"
	"fmt"
	"time"

	"api-go/internal/domain"
)

type readingTable string

const (
	smartMeterReadingsTable    readingTable = "smart_meter_readings"
	batteryBMSReadingsTable    readingTable = "battery_bms_readings"
	solarInverterReadingsTable readingTable = "solar_inverter_readings"
)

// readingTables is the only place a device's hypertable name lives; which
// metrics it reports and how each is aggregated comes from domain.AggregationFor.
var readingTables = map[domain.AssetType]readingTable{
	domain.AssetTypeSmartMeter:    smartMeterReadingsTable,
	domain.AssetTypeBatteryBMS:    batteryBMSReadingsTable,
	domain.AssetTypeSolarInverter: solarInverterReadingsTable,
}

type readingSource struct {
	table       readingTable
	column      string
	aggregation domain.ReadingAggregation
}

func (s *Store) GetReadingSeries(ctx context.Context, query domain.ReadingQuery) (domain.ReadingSeries, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	source, ok := readingSourceFor(query.AssetType, query.Metric)
	if !ok {
		return domain.ReadingSeries{}, fmt.Errorf("no readings source for asset type %q and metric %q", query.AssetType, query.Metric)
	}
	interval, ok := readingIntervalSQL[query.Interval]
	if !ok {
		return domain.ReadingSeries{}, fmt.Errorf("no SQL interval for %q", query.Interval)
	}

	rows, err := s.pool.Query(ctx, buildReadingSeriesQuery(source), interval, query.AssetID, query.From, query.To)
	if err != nil {
		return domain.ReadingSeries{}, fmt.Errorf("query readings: %w", err)
	}
	defer rows.Close()

	points := make([]domain.ReadingPoint, 0)
	for rows.Next() {
		var (
			bucket time.Time
			value  *float64
		)
		if err := rows.Scan(&bucket, &value); err != nil {
			return domain.ReadingSeries{}, fmt.Errorf("read readings: %w", err)
		}
		if point, ok := readingPoint(bucket, value); ok {
			points = append(points, point)
		}
	}
	if err := rows.Err(); err != nil {
		return domain.ReadingSeries{}, fmt.Errorf("read readings: %w", err)
	}

	return domain.ReadingSeries{
		AssetID:     query.AssetID,
		AssetType:   query.AssetType,
		From:        query.From,
		To:          query.To,
		Metric:      query.Metric,
		Interval:    query.Interval,
		Aggregation: source.aggregation,
		Points:      points,
	}, nil
}

var readingIntervalSQL = map[domain.ReadingInterval]string{
	domain.ReadingIntervalOneMinute:     "1 minute",
	domain.ReadingIntervalFiveMinutes:   "5 minutes",
	domain.ReadingIntervalFifteenMinute: "15 minutes",
	domain.ReadingIntervalOneHour:       "1 hour",
}

func readingSourceFor(assetType domain.AssetType, metric domain.ReadingMetric) (readingSource, bool) {
	table, ok := readingTables[assetType]
	if !ok {
		return readingSource{}, false
	}
	aggregation, ok := domain.AggregationFor(assetType, metric)
	if !ok {
		return readingSource{}, false
	}
	return readingSource{table: table, column: string(metric), aggregation: aggregation}, true
}

func readingPoint(bucket time.Time, value *float64) (domain.ReadingPoint, bool) {
	if value == nil {
		return domain.ReadingPoint{}, false
	}
	return domain.ReadingPoint{Time: bucket, Value: *value}, true
}

func buildReadingSeriesQuery(source readingSource) string {
	aggregate := "avg"
	if source.aggregation == domain.ReadingAggregationMaximum {
		aggregate = "max"
	}
	return fmt.Sprintf(`
SELECT time_bucket($1::interval, time) AS time,
       %s(%s)::double precision AS value
FROM %s
WHERE asset_id = $2
  AND time >= $3
  AND time < $4
  AND %s IS NOT NULL
GROUP BY 1
ORDER BY 1`, aggregate, source.column, source.table, source.column)
}
