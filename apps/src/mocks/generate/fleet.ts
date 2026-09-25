import { CRITICAL_TEMPERATURE_C } from "#/api/contract";
import type { Asset, AssetType, ReadingMetric, Site } from "#/api/types";
import siteRows from "../fixtures/sites.json";
import { smoothNoise } from "./noise";

// Mirrors scripts/virtual_meter.go with -site-prefix ng-demo: demo site IDs,
// met-SSMM meter IDs, house-SSMM households, 5s reports, 10 meters per site.
// Unlike the simulator, each site also gets one battery BMS and one solar
// inverter so every asset type the API serves is exercised, and the fleet
// extends past the simulator's five named sites to fill the command surface.

export const REPORT_INTERVAL_MS = 5_000;
const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;
const NIGERIA_UTC_OFFSET_H = 1;

export const BOOT = Math.floor(Date.now() / MINUTE) * MINUTE;

// The first five keep the simulator's numbering (met-01xx is rivers-bolo).
const SITE_ORDER = [
	"rivers-bolo",
	"lagos-epe",
	"imo-ohaji",
	"nasarawa-duduguru",
	"bayelsa-oweikorogha",
	"akwa-ibom-ibeno",
	"bauchi-darazo",
	"benue-otukpo",
	"borno-monguno",
	"cross-river-ikom",
	"delta-burutu",
	"ebonyi-abakaliki",
	"enugu-nsukka",
	"kaduna-kachia",
	"kano-bagwai",
	"kwara-patigi",
	"niger-wushishi",
	"sokoto-illela",
	"taraba-ibi",
	"yobe-geidam",
];
const METERS_PER_SITE = 10;

// Sites whose gateway went quiet: every asset stops reporting at this time.
const SILENT_SITES: Record<string, number> = {
	"bayelsa-oweikorogha": BOOT - 18 * MINUTE,
	"akwa-ibom-ibeno": BOOT - 5 * HOUR,
	"borno-monguno": BOOT - 18 * HOUR,
};

export interface AssetSpec {
	asset_id: string;
	site_id: string;
	asset_type: AssetType;
	household_id?: string;
	registeredAt: number;
	reportsTypeMetrics: boolean;
	disconnectedAt?: number;
	relayUnknown?: boolean;
}

// A window where an asset ran hot. end === null means still hot.
export interface HeatEpisode {
	asset_id: string;
	start: number;
	end: number | null;
	peak: number;
	operatorResolution?: { actor: string; note: string };
}

export interface WarningAlertSpec {
	asset_id: string;
	kind: string;
	reason: string;
	openedAt: number;
}

const REGISTERED_AT = BOOT - 400 * DAY;

const METER_OVERRIDES: Record<string, Partial<AssetSpec>> = {
	"met-0204": { disconnectedAt: BOOT - 3 * DAY },
	"met-0207": { disconnectedAt: BOOT - 9 * HOUR },
	"met-0209": { relayUnknown: true },
	"met-0310": { reportsTypeMetrics: false, registeredAt: BOOT - 2 * HOUR },
	"met-1305": { disconnectedAt: BOOT - 2 * HOUR },
	"met-1508": { disconnectedAt: BOOT - 26 * HOUR },
};

function buildAssets(): AssetSpec[] {
	const assets: AssetSpec[] = [];
	SITE_ORDER.forEach((site_id, index) => {
		const site = String(index + 1).padStart(2, "0");
		const base = {
			site_id,
			registeredAt: REGISTERED_AT,
			reportsTypeMetrics: true,
		};
		for (let m = 1; m <= METERS_PER_SITE; m++) {
			const asset_id = `met-${site}${String(m).padStart(2, "0")}`;
			assets.push({
				...base,
				asset_id,
				asset_type: "smart_meter",
				household_id: `house-${site}${String(m).padStart(2, "0")}`,
				...METER_OVERRIDES[asset_id],
			});
		}
		assets.push({
			...base,
			asset_id: `bms-${site}01`,
			asset_type: "battery_bms",
		});
		assets.push({
			...base,
			asset_id: `inv-${site}01`,
			asset_type: "solar_inverter",
		});
	});
	return assets.sort((a, b) => a.asset_id.localeCompare(b.asset_id));
}

export const ASSETS = buildAssets();
export const ASSETS_BY_ID = new Map(ASSETS.map((a) => [a.asset_id, a]));

const atDayOffset = (days: number, hourUtc: number) =>
	Math.floor(BOOT / DAY) * DAY - days * DAY + hourUtc * HOUR;

export const HEAT_EPISODES: HeatEpisode[] = [
	{
		asset_id: "met-0104",
		start: atDayOffset(6, 12),
		end: atDayOffset(6, 12) + 50 * MINUTE,
		peak: 71.2,
	},
	{
		asset_id: "met-0104",
		start: atDayOffset(4, 13),
		end: atDayOffset(4, 13) + 70 * MINUTE,
		peak: 73.8,
		operatorResolution: { actor: "operator-0101", note: "fan cleaned" },
	},
	{
		asset_id: "met-0104",
		start: atDayOffset(2, 14),
		end: atDayOffset(2, 14) + 40 * MINUTE,
		peak: 71.9,
	},
	{
		asset_id: "met-0104",
		start: atDayOffset(1, 13),
		end: atDayOffset(1, 13) + 30 * MINUTE,
		peak: 70.6,
	},
	{ asset_id: "met-0104", start: BOOT - 25 * MINUTE, end: null, peak: 72.4 },
	{
		asset_id: "met-0302",
		start: atDayOffset(8, 15),
		end: atDayOffset(8, 15) + 35 * MINUTE,
		peak: 70.9,
	},
	{
		asset_id: "bms-0401",
		start: atDayOffset(3, 14),
		end: atDayOffset(3, 14) + 90 * MINUTE,
		peak: 58.3,
	},
	{ asset_id: "bms-0401", start: BOOT - 12 * MINUTE, end: null, peak: 57.1 },
	{
		asset_id: "inv-0201",
		start: atDayOffset(5, 13),
		end: atDayOffset(5, 13) + 45 * MINUTE,
		peak: 78.4,
	},
	{ asset_id: "met-0507", start: BOOT - 40 * MINUTE, end: null, peak: 71.5 },
	{
		asset_id: "met-0104",
		start: BOOT - 5 * HOUR,
		end: BOOT - 4 * HOUR - 30 * MINUTE,
		peak: 71.1,
	},
	{
		asset_id: "inv-1101",
		start: BOOT - 9 * HOUR,
		end: BOOT - 8 * HOUR - 20 * MINUTE,
		peak: 76.2,
	},
	{
		asset_id: "bms-1601",
		start: BOOT - 6 * HOUR,
		end: BOOT - 5 * HOUR,
		peak: 57.8,
		operatorResolution: {
			actor: "operator-0102",
			note: "shade cloth refitted, intake vent cleared",
		},
	},
	{
		asset_id: "inv-1201",
		start: BOOT - 95 * MINUTE,
		end: BOOT - 60 * MINUTE,
		peak: 77.0,
	},
	{ asset_id: "bms-1901", start: BOOT - 20 * MINUTE, end: null, peak: 56.4 },
	{ asset_id: "met-0903", start: BOOT - 19 * HOUR, end: null, peak: 72.8 },
];

export const WARNING_ALERTS: WarningAlertSpec[] = [
	{
		asset_id: "bms-1001",
		kind: "battery_soc_low",
		reason: "battery_soc_low:22.4%<threshold:25.0%",
		openedAt: BOOT - 35 * MINUTE,
	},
	{
		asset_id: "met-1406",
		kind: "frequency_drift",
		reason: "frequency_drift:49.71Hz<lower_bound:49.80Hz",
		openedAt: BOOT - 52 * MINUTE,
	},
	{
		asset_id: "inv-1701",
		kind: "solar_irradiance_low",
		reason: "solar_irradiance_low:118.2W/m2<expected:180.0W/m2",
		openedAt: BOOT - 74 * MINUTE,
	},
	{
		asset_id: "met-2003",
		kind: "voltage_sag",
		reason: "voltage_sag:207.8V<lower_bound:210.0V",
		openedAt: BOOT - 96 * MINUTE,
	},
];

const RAMP_MS = 8 * MINUTE;

// Offsets each asset's report within the 5s interval like independent devices.
const reportOffset = (assetId: string) =>
	Math.floor(smoothNoise(`offset:${assetId}`, 0, 1) * REPORT_INTERVAL_MS);

export function lastReportAt(spec: AssetSpec, now: number): number {
	const silentAt = SILENT_SITES[spec.site_id];
	const until = silentAt !== undefined ? Math.min(now, silentAt) : now;
	const offset = reportOffset(spec.asset_id);
	return (
		Math.floor((until - offset) / REPORT_INTERVAL_MS) * REPORT_INTERVAL_MS +
		offset
	);
}

const localHour = (t: number) =>
	(((t / HOUR + NIGERIA_UTC_OFFSET_H) % 24) + 24) % 24;

// Evening-peaked household demand, 0..1.
const loadShape = (t: number) => {
	const h = localHour(t);
	const evening = Math.exp(-((h - 20) ** 2) / 6);
	const morning = 0.45 * Math.exp(-((h - 7) ** 2) / 3);
	return 0.15 + 0.85 * Math.max(evening, morning);
};

const daylight = (t: number) => {
	const h = localHour(t);
	return h > 6 && h < 18 ? Math.sin(((h - 6) / 12) * Math.PI) : 0;
};

function heatBump(spec: AssetSpec, t: number, base: number): number {
	let bump = 0;
	for (const e of HEAT_EPISODES) {
		if (e.asset_id !== spec.asset_id || t < e.start) continue;
		const end = e.end ?? Number.POSITIVE_INFINITY;
		if (t > end + RAMP_MS) continue;
		const rise = Math.min(1, (t - e.start) / RAMP_MS);
		const fall = t > end ? 1 - (t - end) / RAMP_MS : 1;
		bump = Math.max(bump, (e.peak - base) * Math.min(rise, fall));
	}
	return bump;
}

function baseTemperature(spec: AssetSpec, t: number): number {
	const n = smoothNoise(`temp:${spec.asset_id}`, t, 20 * MINUTE);
	switch (spec.asset_type) {
		case "smart_meter":
			return 30 + 8 * n + 4 * daylight(t);
		case "battery_bms":
			return 27 + 5 * n + 6 * daylight(t);
		case "solar_inverter":
			return 32 + 6 * n + 22 * daylight(t);
	}
}

export function isRelayClosed(spec: AssetSpec, t: number): boolean {
	return spec.disconnectedAt === undefined || t < spec.disconnectedAt;
}

export function sample(
	spec: AssetSpec,
	metric: ReadingMetric,
	t: number,
): number {
	const key = `${metric}:${spec.asset_id}`;
	switch (metric) {
		case "internal_temperature": {
			const base = baseTemperature(spec, t);
			const bump = heatBump(spec, t, base);
			const jitter = bump > 0 ? (smoothNoise(key, t, MINUTE) - 0.5) * 0.8 : 0;
			return base + bump + jitter;
		}
		case "voltage":
			return 215 + 25 * smoothNoise(key, t, 3 * MINUTE);
		case "current":
			if (!isRelayClosed(spec, t)) return 0;
			return (
				0.5 +
				17.5 * loadShape(t) * (0.55 + 0.45 * smoothNoise(key, t, 4 * MINUTE))
			);
		case "active_power":
			return (sample(spec, "voltage", t) * sample(spec, "current", t)) / 1000;
		case "frequency":
			return 49.8 + 0.4 * smoothNoise(key, t, 2 * MINUTE);
		case "total_kwh": {
			// Mean household draw ~1.4 kW integrated since registration; the
			// counter holds once the relay opens.
			const until = Math.min(t, spec.disconnectedAt ?? t);
			const hours = Math.max(0, until - spec.registeredAt) / HOUR;
			const seed = smoothNoise(`kwh:${spec.asset_id}`, 0, 1);
			return 120 + 400 * seed + hours * (0.9 + 0.5 * seed);
		}
		case "battery_soc_pct": {
			const h = localHour(t);
			const cycle = Math.sin(((h - 10) / 24) * 2 * Math.PI);
			const soc =
				64 + 28 * cycle + 6 * (smoothNoise(key, t, 30 * MINUTE) - 0.5);
			return Math.max(12, Math.min(99, soc));
		}
		case "solar_irradiance": {
			const cloud = 0.62 + 0.38 * smoothNoise(key, t, 25 * MINUTE);
			return 850 * daylight(t) * cloud;
		}
	}
}

const round = (value: number, digits: number) => {
	const f = 10 ** digits;
	return Math.round(value * f) / f;
};

export function assetStateAt(spec: AssetSpec, now: number): Asset {
	const seenAt = lastReportAt(spec, now);
	const seen = new Date(seenAt).toISOString();
	const asset: Asset = {
		asset_id: spec.asset_id,
		site_id: spec.site_id,
		asset_type: spec.asset_type,
		internal_temperature: round(
			sample(spec, "internal_temperature", seenAt),
			1,
		),
		last_seen_at: seen,
		updated_at: seen,
	};
	if (!spec.reportsTypeMetrics) return asset;

	switch (spec.asset_type) {
		case "smart_meter":
			asset.smart_meter = {
				reported_household_id: spec.household_id ?? null,
				relay_closed: spec.relayUnknown ? null : isRelayClosed(spec, seenAt),
				voltage: round(sample(spec, "voltage", seenAt), 1),
				current: round(sample(spec, "current", seenAt), 2),
				active_power: round(sample(spec, "active_power", seenAt), 3),
				frequency: round(sample(spec, "frequency", seenAt), 2),
				total_kwh: round(sample(spec, "total_kwh", seenAt), 2),
				updated_at: seen,
			};
			break;
		case "battery_bms":
			asset.battery_bms = {
				battery_soc_pct: round(sample(spec, "battery_soc_pct", seenAt), 1),
				updated_at: seen,
			};
			break;
		case "solar_inverter":
			asset.solar_inverter = {
				solar_irradiance: round(sample(spec, "solar_irradiance", seenAt), 1),
				updated_at: seen,
			};
			break;
	}
	return asset;
}

export const siteRowsById = new Map(
	(
		siteRows as Omit<Site, "asset_count" | "last_seen_at" | "open_alerts">[]
	).map((row) => [row.site_id, row]),
);

export const criticalFor = (spec: AssetSpec) =>
	CRITICAL_TEMPERATURE_C[spec.asset_type];
