// mirrors services/api-go/internal/controller/dto.go and response.go.

export type AssetType = "smart_meter" | "battery_bms" | "solar_inverter";
export type Severity = "info" | "warning" | "critical";
export type AlertStatus = "open" | "resolved";
export type ReadingInterval = "1m" | "5m" | "15m" | "1h";
export type ReadingAggregation = "avg" | "max";
export type ReadingMetric =
	| "internal_temperature"
	| "voltage"
	| "current"
	| "active_power"
	| "frequency"
	| "total_kwh"
	| "battery_soc_pct"
	| "solar_irradiance";

export interface PageMetadata {
	limit: number;
	next_cursor: string;
}

export interface CollectionBody<T> {
	data: T[];
	page: PageMetadata;
	request_id: string;
}

export interface ObjectBody<T> {
	data: T;
	request_id: string;
}

export type ErrorCode =
	| "invalid_argument"
	| "unauthenticated"
	| "not_found"
	| "conflict"
	| "method_not_allowed"
	| "deadline_exceeded"
	| "internal";

export interface ErrorBody {
	error: { code: ErrorCode; message: string; request_id: string };
}

export interface AlertCounts {
	total: number;
	critical: number;
	warning: number;
	info: number;
}

export interface Site {
	site_id: string;
	name: string;
	country: string | null;
	region: string | null;
	operator_id: string | null;
	lat: number | null;
	lng: number | null;
	status: string;
	asset_count: number;
	last_seen_at: string | null;
	open_alerts: AlertCounts;
}

export interface SmartMeterState {
	reported_household_id: string | null;
	relay_closed: boolean | null;
	voltage: number | null;
	current: number | null;
	active_power: number | null;
	frequency: number | null;
	total_kwh: number | null;
	updated_at: string;
}

export interface BatteryBMSState {
	battery_soc_pct: number | null;
	updated_at: string;
}

export interface SolarInverterState {
	solar_irradiance: number | null;
	updated_at: string;
}

// exactly one type block is present once the asset has reported type metrics;
// none is present before that.
export interface Asset {
	asset_id: string;
	site_id: string;
	asset_type: AssetType;
	internal_temperature: number | null;
	last_seen_at: string;
	updated_at: string;
	smart_meter?: SmartMeterState;
	battery_bms?: BatteryBMSState;
	solar_inverter?: SolarInverterState;
}

export interface ReadingPoint {
	time: string;
	value: number;
}

export interface ReadingSeries {
	asset_id: string;
	asset_type: AssetType;
	from: string;
	to: string;
	metric: ReadingMetric;
	interval: ReadingInterval;
	aggregation: ReadingAggregation;
	points: ReadingPoint[];
}

export interface Alert {
	alert_id: string;
	asset_id: string;
	site_id: string;
	kind: string;
	severity: Severity;
	status: AlertStatus;
	reason: string;
	opened_at: string;
	source_event_id: string | null;
	resolved_at: string | null;
	resolution_note: string | null;
	resolved_by: string | null;
}

export interface AlertResolution {
	resolution_id: string;
	resolved_at: string;
	resolution_note: string;
	resolved_by: string;
	recorded_at: string;
}

export interface AlertDetail extends Alert {
	resolutions: AlertResolution[];
}
