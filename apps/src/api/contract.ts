import type {
	AssetType,
	ReadingAggregation,
	ReadingInterval,
	ReadingMetric,
} from "./types";

// Mirrors services/api-go/internal/domain/readings.go and config defaults.

export const DEFAULT_PAGE_SIZE = 50;
export const MAX_PAGE_SIZE = 200;
export const GLOBAL_HISTORY_PAGE_SIZE = 25;
export const ACTOR_ID_HEADER = "X-Actor-Id";
export const SYSTEM_ACTOR = "system";

const HOUR_MS = 60 * 60 * 1000;
const DAY_MS = 24 * HOUR_MS;

export const INTERVAL_MS: Record<ReadingInterval, number> = {
	"1m": 60 * 1000,
	"5m": 5 * 60 * 1000,
	"15m": 15 * 60 * 1000,
	"1h": HOUR_MS,
};

export const INTERVAL_MAX_WINDOW_MS: Record<ReadingInterval, number> = {
	"1m": DAY_MS,
	"5m": 7 * DAY_MS,
	"15m": 30 * DAY_MS,
	"1h": 366 * DAY_MS,
};

export const ASSET_METRICS: Record<
	AssetType,
	{ metric: ReadingMetric; aggregation: ReadingAggregation }[]
> = {
	smart_meter: [
		{ metric: "internal_temperature", aggregation: "avg" },
		{ metric: "voltage", aggregation: "avg" },
		{ metric: "current", aggregation: "avg" },
		{ metric: "active_power", aggregation: "avg" },
		{ metric: "frequency", aggregation: "avg" },
		{ metric: "total_kwh", aggregation: "max" },
	],
	battery_bms: [
		{ metric: "internal_temperature", aggregation: "avg" },
		{ metric: "battery_soc_pct", aggregation: "avg" },
	],
	solar_inverter: [
		{ metric: "internal_temperature", aggregation: "avg" },
		{ metric: "solar_irradiance", aggregation: "avg" },
	],
};

// Mirrors services/engine-rust/src/domain/rules.rs ThresholdPolicy defaults.
export const CRITICAL_TEMPERATURE_C: Record<AssetType, number> = {
	smart_meter: 70,
	battery_bms: 55,
	solar_inverter: 75,
};
export const RECOVERY_MARGIN_C = 5;

export const METRIC_LABEL: Record<
	ReadingMetric,
	{ label: string; unit: string }
> = {
	internal_temperature: { label: "Internal temperature", unit: "°C" },
	voltage: { label: "Voltage", unit: "V" },
	current: { label: "Current", unit: "A" },
	active_power: { label: "Active power", unit: "kW" },
	frequency: { label: "Frequency", unit: "Hz" },
	total_kwh: { label: "Total energy", unit: "kWh" },
	battery_soc_pct: { label: "State of charge", unit: "%" },
	solar_irradiance: { label: "Solar irradiance", unit: "W/m²" },
};

export const ASSET_TYPE_LABEL: Record<AssetType, string> = {
	smart_meter: "Smart meter",
	battery_bms: "Battery BMS",
	solar_inverter: "Solar inverter",
};

export interface RangePreset {
	id: string;
	label: string;
	windowMs: number;
	interval: ReadingInterval;
}

// Each preset uses the finest interval whose window cap covers it, so no
// preset can produce a 400.
export const RANGE_PRESETS: RangePreset[] = [
	{ id: "1h", label: "Last hour", windowMs: HOUR_MS, interval: "1m" },
	{ id: "24h", label: "Last 24 hours", windowMs: DAY_MS, interval: "1m" },
	{ id: "7d", label: "Last 7 days", windowMs: 7 * DAY_MS, interval: "5m" },
	{ id: "30d", label: "Last 30 days", windowMs: 30 * DAY_MS, interval: "15m" },
	{ id: "1y", label: "Last year", windowMs: 365 * DAY_MS, interval: "1h" },
];
