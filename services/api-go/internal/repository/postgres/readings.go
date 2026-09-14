// Reads bounded, downsampled telemetry series from TimescaleDB hypertables.
package postgres

import (
	"context"
	"fmt"
	"time"

	"api-go/internal/domain"
)

type readingSource struct {
	table       string
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
	base := readingSource{aggregation: domain.ReadingAggregationAverage}

	switch assetType {
	case domain.AssetTypeSmartMeter:
		base.table = "smart_meter_readings"
		switch metric {
		case domain.ReadingMetricInternalTemperature,
			domain.ReadingMetricVoltage,
			domain.ReadingMetricCurrent,
			domain.ReadingMetricActivePower,
			domain.ReadingMetricFrequency:
			base.column = string(metric)
		case domain.ReadingMetricTotalKWh:
			base.column = string(metric)
			base.aggregation = domain.ReadingAggregationMaximum
		}
	case domain.AssetTypeBatteryBMS:
		base.table = "battery_bms_readings"
		switch metric {
		case domain.ReadingMetricInternalTemperature, domain.ReadingMetricBatterySOCPct:
			base.column = string(metric)
		}
	case domain.AssetTypeSolarInverter:
		base.table = "solar_inverter_readings"
		switch metric {
		case domain.ReadingMetricInternalTemperature, domain.ReadingMetricSolarIrradiance:
			base.column = string(metric)
		}
	}

	if base.column == "" {
		return readingSource{}, false
	}
	return base, true
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
