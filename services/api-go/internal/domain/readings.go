// defines chart-ready historical telemetry queries and results.
package domain

import (
	"fmt"
	"time"
)

type ReadingMetric string

const (
	ReadingMetricInternalTemperature ReadingMetric = "internal_temperature"
	ReadingMetricVoltage             ReadingMetric = "voltage"
	ReadingMetricCurrent             ReadingMetric = "current"
	ReadingMetricActivePower         ReadingMetric = "active_power"
	ReadingMetricFrequency           ReadingMetric = "frequency"
	ReadingMetricTotalKWh            ReadingMetric = "total_kwh"
	ReadingMetricBatterySOCPct       ReadingMetric = "battery_soc_pct"
	ReadingMetricSolarIrradiance     ReadingMetric = "solar_irradiance"
)

func ParseReadingMetric(value string) (ReadingMetric, error) {
	metric := ReadingMetric(value)
	if _, ok := readingMetricAssetTypes[metric]; !ok {
		return "", fmt.Errorf("unknown metric: %q", value)
	}
	return metric, nil
}

var readingMetricAssetTypes = map[ReadingMetric]map[AssetType]struct{}{
	ReadingMetricInternalTemperature: {
		AssetTypeSmartMeter: {}, AssetTypeBatteryBMS: {}, AssetTypeSolarInverter: {},
	},
	ReadingMetricVoltage:         {AssetTypeSmartMeter: {}},
	ReadingMetricCurrent:         {AssetTypeSmartMeter: {}},
	ReadingMetricActivePower:     {AssetTypeSmartMeter: {}},
	ReadingMetricFrequency:       {AssetTypeSmartMeter: {}},
	ReadingMetricTotalKWh:        {AssetTypeSmartMeter: {}},
	ReadingMetricBatterySOCPct:   {AssetTypeBatteryBMS: {}},
	ReadingMetricSolarIrradiance: {AssetTypeSolarInverter: {}},
}

func (m ReadingMetric) Supports(assetType AssetType) bool {
	_, ok := readingMetricAssetTypes[m][assetType]
	return ok
}

type ReadingInterval string

const (
	ReadingIntervalOneMinute     ReadingInterval = "1m"
	ReadingIntervalFiveMinutes   ReadingInterval = "5m"
	ReadingIntervalFifteenMinute ReadingInterval = "15m"
	ReadingIntervalOneHour       ReadingInterval = "1h"
)

func ParseReadingInterval(value string) (ReadingInterval, error) {
	interval := ReadingInterval(value)
	if _, ok := readingIntervalMaximumWindows[interval]; !ok {
		return "", fmt.Errorf("unsupported interval: %q", value)
	}
	return interval, nil
}

var readingIntervalMaximumWindows = map[ReadingInterval]time.Duration{
	ReadingIntervalOneMinute:     24 * time.Hour,
	ReadingIntervalFiveMinutes:   7 * 24 * time.Hour,
	ReadingIntervalFifteenMinute: 30 * 24 * time.Hour,
	ReadingIntervalOneHour:       366 * 24 * time.Hour,
}

func (i ReadingInterval) MaximumWindow() time.Duration {
	return readingIntervalMaximumWindows[i]
}

type ReadingAggregation string

const (
	ReadingAggregationAverage ReadingAggregation = "avg"
	ReadingAggregationMaximum ReadingAggregation = "max"
)

type ReadingQuery struct {
	AssetID   string
	AssetType AssetType
	From      time.Time
	To        time.Time
	Metric    ReadingMetric
	Interval  ReadingInterval
}

type ReadingPoint struct {
	Time  time.Time
	Value float64
}

type ReadingSeries struct {
	AssetID     string
	AssetType   AssetType
	From        time.Time
	To          time.Time
	Metric      ReadingMetric
	Interval    ReadingInterval
	Aggregation ReadingAggregation
	Points      []ReadingPoint
}
