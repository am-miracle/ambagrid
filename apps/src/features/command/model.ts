import { ASSET_TYPE_LABEL } from "#/api/contract";
import type { Alert, AssetType, Severity, Site } from "#/api/types";
import { describeReason, humanizeKind } from "#/lib/format";
import { STALE_AFTER_MS } from "#/lib/grid-tick";
import type { AssetTelemetry, SiteTelemetrySnapshot } from "#/lib/telemetry";

export type Status = "healthy" | "warning" | "critical" | "offline";

export const STATUS_ORDER: Status[] = [
	"critical",
	"warning",
	"healthy",
	"offline",
];

export const STATUS_COLOR: Record<Status, string> = {
	healthy: "#39D7C0",
	warning: "#F5A524",
	critical: "#FF4D5E",
	offline: "#5A6E82",
};

export const ASSET_TYPES: AssetType[] = [
	"smart_meter",
	"battery_bms",
	"solar_inverter",
];

export const REPLAY_WINDOW_MS = 12 * 60 * 60 * 1000;

// Simulator ceiling: 240 V x 18 A per meter.
const MAX_METER_KW = 4.32;
const FULL_SUN_IRRADIANCE = 850;

export interface FleetSite {
	id: string;
	name: string;
	region: string;
	operatorId: string | null;
	lat: number | null;
	lon: number | null;
	provisioningStatus: string;
	lastSeenAt: number | null;
	assets: AssetTelemetry[];
	meters: AssetTelemetry[];
	battery?: AssetTelemetry;
	inverter?: AssetTelemetry;
	loadKw: number;
	loadRatio: number;
	soc: number | null;
	solarRatio: number | null;
	irradiance: number | null;
	disconnected: number;
}

export interface FleetEvent {
	at: number;
	siteId: string;
	status: Status;
	text: string;
}

export const openedAt = (a: Alert) => Date.parse(a.opened_at);
export const resolvedAt = (a: Alert) =>
	a.resolved_at ? Date.parse(a.resolved_at) : null;

export function isOpenAt(alert: Alert, t: number): boolean {
	const resolved = resolvedAt(alert);
	return openedAt(alert) <= t && (resolved === null || resolved > t);
}

export function severityStatus(severity: Severity): Status {
	return severity === "info" ? "healthy" : severity;
}

// Site metadata comes from the slow /v1/sites rollup; everything live comes
// from telemetry snapshots, so it moves at the transport's pace.
export function buildSites(
	sites: Site[],
	snapshots: ReadonlyMap<string, SiteTelemetrySnapshot>,
): FleetSite[] {
	return sites.map((site) => {
		const assets = snapshots.get(site.site_id)?.assets ?? [];
		const meters = assets.filter((a) => a.assetType === "smart_meter");
		const battery = assets.find((a) => a.assetType === "battery_bms");
		const inverter = assets.find((a) => a.assetType === "solar_inverter");
		const loadKw = meters.reduce((sum, m) => sum + (m.activePower ?? 0), 0);
		const socPct = battery?.batterySocPct ?? null;
		const irradiance = inverter?.solarIrradiance ?? null;
		const observed = assets.map((a) => Date.parse(a.observedAt));
		const rollupSeen = site.last_seen_at ? Date.parse(site.last_seen_at) : null;
		return {
			id: site.site_id,
			name: site.name,
			region: site.region ?? site.country ?? "Unassigned",
			operatorId: site.operator_id,
			lat: site.lat,
			lon: site.lng,
			provisioningStatus: site.status,
			lastSeenAt: observed.length ? Math.max(...observed) : rollupSeen,
			assets,
			meters,
			battery,
			inverter,
			loadKw,
			loadRatio: meters.length ? loadKw / (meters.length * MAX_METER_KW) : 0,
			soc: socPct === null ? null : socPct / 100,
			solarRatio: irradiance === null ? null : irradiance / FULL_SUN_IRRADIANCE,
			irradiance,
			disconnected: meters.filter((m) => m.relayClosed === false).length,
		};
	});
}

// Historical offline state is inferred from last_seen_at: a site that last
// reported before t minus the stale window was already dark at t. The API
// keeps no reporting history, so a site that went dark and recovered inside
// the window replays as online.
export function siteStatusAt(
	site: FleetSite,
	alerts: Alert[],
	t: number,
): Status {
	if (site.lastSeenAt === null || site.lastSeenAt < t - STALE_AFTER_MS) {
		return "offline";
	}
	let status: Status = "healthy";
	for (const alert of alerts) {
		if (alert.site_id !== site.id || !isOpenAt(alert, t)) continue;
		if (alert.severity === "critical") return "critical";
		if (alert.severity === "warning") status = "warning";
	}
	return status;
}

export function assetFreshAt(asset: AssetTelemetry, t: number): boolean {
	return Date.parse(asset.observedAt) >= t - STALE_AFTER_MS;
}

export function alertTitle(alert: Alert, assetType?: AssetType): string {
	if (alert.kind === "internal_temperature") {
		return `${assetType ? ASSET_TYPE_LABEL[assetType] : "Asset"} over-temperature`;
	}
	return humanizeKind(alert.kind);
}

export const alertSummary = (alert: Alert) => describeReason(alert.reason);

export const shortId = (id: string) => id.slice(0, 8);

export function buildEvents(
	alerts: Alert[],
	sitesById: Map<string, FleetSite>,
	from: number,
	to: number,
): FleetEvent[] {
	const events: FleetEvent[] = [];
	for (const alert of alerts) {
		const site = sitesById.get(alert.site_id)?.name ?? alert.site_id;
		const opened = openedAt(alert);
		if (opened >= from && opened <= to) {
			events.push({
				at: opened,
				siteId: alert.site_id,
				status: severityStatus(alert.severity),
				text: `${site} · ${alert.asset_id} ${humanizeKind(alert.kind).toLowerCase()} alert opened`,
			});
		}
		const resolved = resolvedAt(alert);
		if (resolved !== null && resolved >= from && resolved <= to) {
			events.push({
				at: resolved,
				siteId: alert.site_id,
				status: "healthy",
				text:
					alert.resolved_by === "system"
						? `${site} · ${alert.asset_id} recovered`
						: `${site} · ${alert.asset_id} resolved by ${alert.resolved_by}`,
			});
		}
	}
	return events.sort((a, b) => a.at - b.at);
}

// Status runs for one site across the window, sampled every five minutes.
export function statusBands(
	site: FleetSite,
	alerts: Alert[],
	from: number,
	to: number,
) {
	const step = 5 * 60 * 1000;
	const bands: { from: number; to: number; status: Status }[] = [];
	for (let t = from; t < to; t += step) {
		const status = siteStatusAt(site, alerts, t);
		const last = bands[bands.length - 1];
		if (last && last.status === status) last.to = Math.min(t + step, to);
		else bands.push({ from: t, to: Math.min(t + step, to), status });
	}
	return bands;
}

// Decorative mesh joining each placed site to its two nearest neighbours. It
// is not a network topology; the API has no backhaul model.
export function proximityLinks(sites: FleetSite[]): [string, string][] {
	const placed = sites.filter((s) => s.lat !== null && s.lon !== null);
	const seen = new Set<string>();
	const links: [string, string][] = [];
	for (const a of placed) {
		const nearest = placed
			.filter((b) => b.id !== a.id)
			.map((b) => ({
				b,
				d:
					((b.lat ?? 0) - (a.lat ?? 0)) ** 2 +
					((b.lon ?? 0) - (a.lon ?? 0)) ** 2,
			}))
			.sort((x, y) => x.d - y.d)
			.slice(0, 2);
		for (const { b } of nearest) {
			const key = [a.id, b.id].sort().join("|");
			if (seen.has(key)) continue;
			seen.add(key);
			links.push([a.id, b.id]);
		}
	}
	return links;
}

const clock = new Intl.DateTimeFormat(undefined, {
	hour: "2-digit",
	minute: "2-digit",
	hour12: false,
});

export const formatClock = (ms: number) => clock.format(new Date(ms));

export const timeZoneLabel =
	new Intl.DateTimeFormat(undefined, { timeZoneName: "short" })
		.formatToParts(new Date())
		.find((p) => p.type === "timeZoneName")?.value ?? "";
