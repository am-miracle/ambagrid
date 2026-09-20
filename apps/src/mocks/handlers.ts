import { delay, HttpResponse, http } from "msw";
import {
	ACTOR_ID_HEADER,
	ASSET_METRICS,
	DEFAULT_PAGE_SIZE,
	GLOBAL_HISTORY_PAGE_SIZE,
	INTERVAL_MAX_WINDOW_MS,
	INTERVAL_MS,
	MAX_PAGE_SIZE,
	SYSTEM_ACTOR,
} from "#/api/contract";
import type {
	Alert,
	AlertDetail,
	AssetType,
	ErrorCode,
	ReadingInterval,
	ReadingMetric,
	Site,
} from "#/api/types";
import { decodeCursor, encodeCursor } from "./cursor";
import {
	allAlerts,
	evaluatePolicy,
	resolveByOperator,
} from "./generate/alerts";
import {
	ASSETS,
	ASSETS_BY_ID,
	assetStateAt,
	lastReportAt,
	sample,
	siteRowsById,
} from "./generate/fleet";

const BASE = `${import.meta.env.VITE_API_BASE_URL ?? "http://localhost:8081"}/v1`;

class ApiFailure extends Error {
	constructor(
		readonly status: number,
		readonly code: ErrorCode,
		message: string,
	) {
		super(message);
	}
}

const invalidParameter = (detail: string) =>
	new ApiFailure(
		400,
		"invalid_argument",
		`invalid request parameter: ${detail}`,
	);
const invalidRequest = (detail: string) =>
	new ApiFailure(400, "invalid_argument", `invalid request: ${detail}`);
const invalidCursor = () =>
	new ApiFailure(
		400,
		"invalid_argument",
		"cursor is not a cursor this API issued",
	);
const notFound = () => new ApiFailure(404, "not_found", "object not found");

const requestId = () =>
	Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) =>
		b.toString(16).padStart(2, "0"),
	).join("");

function respond(run: (id: string) => unknown) {
	const id = requestId();
	const headers = { "X-Request-Id": id };
	try {
		return HttpResponse.json(run(id) as object, { headers });
	} catch (error) {
		if (!(error instanceof ApiFailure)) throw error;
		return HttpResponse.json(
			{ error: { code: error.code, message: error.message, request_id: id } },
			{ status: error.status, headers },
		);
	}
}

// Matches optionalParam: absent and blank are the same.
const param = (url: URL, key: string) =>
	url.searchParams.get(key)?.trim() || undefined;

function enumParam<T extends string>(
	url: URL,
	key: string,
	allowed: readonly T[],
): T | undefined {
	const raw = param(url, key);
	if (raw === undefined) return undefined;
	if (!allowed.includes(raw as T)) {
		throw invalidParameter(`unknown ${key}: "${raw}"`);
	}
	return raw as T;
}

function resolveLimit(url: URL, defaultSize: number, maxSize: number): number {
	const raw = param(url, "limit");
	if (raw === undefined) return defaultSize;
	if (!/^[+-]?\d+$/.test(raw))
		throw invalidParameter("limit must be an integer");
	const limit = Number(raw);
	if (limit < 1) throw invalidParameter("limit must be greater than zero");
	if (limit > maxSize) throw invalidRequest(`limit must not exceed ${maxSize}`);
	return limit;
}

function paginate<T>(
	sorted: T[],
	url: URL,
	limit: number,
	fieldCount: number,
	isAfter: (item: T, fields: string[]) => boolean,
	keyOf: (item: T) => string[],
	id: string,
) {
	const token = param(url, "cursor");
	let rows = sorted;
	if (token) {
		const fields = decodeCursor(token, fieldCount);
		if (!fields) throw invalidCursor();
		rows = sorted.filter((item) => isAfter(item, fields));
	}
	const data = rows.slice(0, limit);
	const hasMore = rows.length > limit;
	return {
		data,
		page: {
			limit,
			next_cursor: hasMore ? encodeCursor(...keyOf(data[data.length - 1])) : "",
		},
		request_id: id,
	};
}

const ASSET_TYPES: AssetType[] = [
	"smart_meter",
	"battery_bms",
	"solar_inverter",
];
const INTERVALS = Object.keys(INTERVAL_MS) as ReadingInterval[];
const METRICS = [
	...new Set(
		Object.values(ASSET_METRICS).flatMap((m) => m.map((x) => x.metric)),
	),
];

// Go formats time.Duration this way in the window-cap message.
const goDuration = (ms: number) => `${ms / 3_600_000}h0m0s`;

function parseTime(url: URL, key: string): number {
	const raw = param(url, key);
	if (raw === undefined) throw invalidParameter(`${key} is required`);
	const rfc3339 =
		/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;
	const value = Date.parse(raw);
	if (!rfc3339.test(raw) || Number.isNaN(value)) {
		throw invalidParameter(`${key} must be an RFC 3339 timestamp`);
	}
	return value;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

const summary = ({ resolutions: _, ...alert }: AlertDetail): Alert => alert;

// Newest-first, with the same tie-break as ORDER BY opened_at DESC, alert_id DESC.
const byNewest = (a: Alert, b: Alert) =>
	b.opened_at.localeCompare(a.opened_at) ||
	b.alert_id.localeCompare(a.alert_id);

export const handlers = [
	http.all(`${BASE}/*`, async () => {
		await delay(120 + Math.random() * 180);
	}),

	http.get(`${BASE}/sites`, ({ request }) =>
		respond((id) => {
			const url = new URL(request.url);
			const limit = resolveLimit(url, DEFAULT_PAGE_SIZE, MAX_PAGE_SIZE);
			const now = Date.now();
			evaluatePolicy(now);
			const open = allAlerts().filter((a) => a.status === "open");

			const sites: Site[] = [...siteRowsById.values()]
				.sort((a, b) => a.site_id.localeCompare(b.site_id))
				.map((row) => {
					const assets = ASSETS.filter((a) => a.site_id === row.site_id);
					const seen = assets.map((a) => lastReportAt(a, now));
					const alerts = open.filter((a) => a.site_id === row.site_id);
					const count = (severity: string) =>
						alerts.filter((a) => a.severity === severity).length;
					return {
						...row,
						asset_count: assets.length,
						last_seen_at: seen.length
							? new Date(Math.max(...seen)).toISOString()
							: null,
						open_alerts: {
							total: alerts.length,
							critical: count("critical"),
							warning: count("warning"),
							info: count("info"),
						},
					};
				});

			return paginate(
				sites,
				url,
				limit,
				1,
				(site, [after]) => site.site_id > after,
				(site) => [site.site_id],
				id,
			);
		}),
	),

	http.get(`${BASE}/assets`, ({ request }) =>
		respond((id) => {
			const url = new URL(request.url);
			const limit = resolveLimit(url, DEFAULT_PAGE_SIZE, MAX_PAGE_SIZE);
			const siteId = param(url, "site_id");
			const assetType = enumParam(url, "asset_type", ASSET_TYPES);
			const now = Date.now();

			const assets = ASSETS.filter(
				(a) =>
					(!siteId || a.site_id === siteId) &&
					(!assetType || a.asset_type === assetType),
			).map((spec) => assetStateAt(spec, now));

			return paginate(
				assets,
				url,
				limit,
				1,
				(asset, [after]) => asset.asset_id > after,
				(asset) => [asset.asset_id],
				id,
			);
		}),
	),

	http.get(`${BASE}/assets/:assetId`, ({ params }) =>
		respond((id) => {
			const spec = ASSETS_BY_ID.get(String(params.assetId));
			if (!spec) throw notFound();
			return { data: assetStateAt(spec, Date.now()), request_id: id };
		}),
	),

	http.get(`${BASE}/assets/:assetId/readings`, ({ request, params }) =>
		respond((id) => {
			const url = new URL(request.url);
			const from = parseTime(url, "from");
			const to = parseTime(url, "to");
			const metric = param(url, "metric");
			if (metric === undefined) throw invalidParameter("metric is required");
			if (!METRICS.includes(metric as ReadingMetric)) {
				throw invalidParameter(`unknown metric: "${metric}"`);
			}
			const interval = param(url, "interval");
			if (interval === undefined)
				throw invalidParameter("interval is required");
			if (!INTERVALS.includes(interval as ReadingInterval)) {
				throw invalidParameter(`unsupported interval: "${interval}"`);
			}
			const step = INTERVAL_MS[interval as ReadingInterval];
			const maxWindow = INTERVAL_MAX_WINDOW_MS[interval as ReadingInterval];

			const assetId = String(params.assetId);
			if (from >= to) throw invalidRequest("from must be before to");
			if (to - from > maxWindow) {
				throw invalidRequest(
					`interval ${interval} supports at most ${goDuration(maxWindow)}`,
				);
			}
			const spec = ASSETS_BY_ID.get(assetId);
			if (!spec) throw notFound();
			const supported = ASSET_METRICS[spec.asset_type].find(
				(m) => m.metric === metric,
			);
			if (!supported) {
				throw invalidRequest(
					`metric "${metric}" is unavailable for asset type "${spec.asset_type}"`,
				);
			}

			const reportedUntil = lastReportAt(spec, Date.now());
			const points: { time: string; value: number }[] = [];
			if (spec.reportsTypeMetrics) {
				const first = Math.floor(from / step) * step;
				for (let bucket = first; bucket < to; bucket += step) {
					const start = Math.max(bucket, from, spec.registeredAt);
					const end = Math.min(bucket + step, to, reportedUntil);
					if (start >= end) continue;
					const samples = [0.1, 0.5, 0.9].map((f) =>
						sample(spec, metric as ReadingMetric, start + (end - start) * f),
					);
					const value =
						supported.aggregation === "max"
							? Math.max(...samples)
							: samples.reduce((sum, v) => sum + v, 0) / samples.length;
					points.push({ time: new Date(bucket).toISOString(), value });
				}
			}

			return {
				data: {
					asset_id: spec.asset_id,
					asset_type: spec.asset_type,
					from: new Date(from).toISOString(),
					to: new Date(to).toISOString(),
					metric,
					interval,
					aggregation: supported.aggregation,
					points,
				},
				request_id: id,
			};
		}),
	),

	http.get(`${BASE}/alerts`, ({ request }) =>
		respond((id) => {
			const url = new URL(request.url);
			const siteId = param(url, "site_id");
			const assetId = param(url, "asset_id");
			let status = enumParam(url, "status", ["open", "resolved"] as const);
			const severity = enumParam(url, "severity", [
				"info",
				"warning",
				"critical",
			] as const);
			const kind = param(url, "kind");

			let limit: number;
			if (!siteId && !assetId && status !== undefined && status !== "open") {
				limit = resolveLimit(
					url,
					GLOBAL_HISTORY_PAGE_SIZE,
					GLOBAL_HISTORY_PAGE_SIZE,
				);
			} else {
				if (!siteId && !assetId) status = "open";
				limit = resolveLimit(url, DEFAULT_PAGE_SIZE, MAX_PAGE_SIZE);
			}

			evaluatePolicy(Date.now());
			const rows = allAlerts()
				.filter(
					(a) =>
						(!siteId || a.site_id === siteId) &&
						(!assetId || a.asset_id === assetId) &&
						(!status || a.status === status) &&
						(!severity || a.severity === severity) &&
						(!kind || a.kind === kind),
				)
				.map(summary)
				.sort(byNewest);

			return paginate(
				rows,
				url,
				limit,
				2,
				(alert, [openedAt, alertId]) =>
					alert.opened_at < openedAt ||
					(alert.opened_at === openedAt && alert.alert_id < alertId),
				(alert) => [alert.opened_at, alert.alert_id],
				id,
			);
		}),
	),

	http.get(`${BASE}/alerts/:alertId`, ({ params }) =>
		respond((id) => {
			const alertId = String(params.alertId);
			if (!UUID.test(alertId)) {
				throw new ApiFailure(
					400,
					"invalid_argument",
					"invalid identifier: alert_id must be a UUID",
				);
			}
			evaluatePolicy(Date.now());
			const alert = allAlerts().find((a) => a.alert_id === alertId);
			if (!alert) throw notFound();
			return { data: alert, request_id: id };
		}),
	),

	http.post(`${BASE}/alerts/:alertId/resolve`, async ({ request, params }) => {
		const actorHeader = request.headers.get(ACTOR_ID_HEADER);
		let body: unknown;
		try {
			body = await request.json();
		} catch {
			body = undefined;
		}

		return respond((id) => {
			if (actorHeader === null) {
				throw new ApiFailure(
					401,
					"unauthenticated",
					"missing trusted actor identity",
				);
			}
			const keys = body && typeof body === "object" ? Object.keys(body) : null;
			const note = (body as { resolution_note?: unknown } | undefined)
				?.resolution_note;
			if (
				!keys ||
				keys.some((k) => k !== "resolution_note") ||
				(note !== undefined && typeof note !== "string")
			) {
				throw new ApiFailure(
					400,
					"invalid_argument",
					"invalid request parameter",
				);
			}
			const alertId = String(params.alertId);
			if (!UUID.test(alertId)) {
				throw new ApiFailure(
					400,
					"invalid_argument",
					"invalid identifier: alert_id must be a UUID",
				);
			}
			const trimmedNote = (note ?? "").trim();
			if (!trimmedNote)
				throw invalidRequest("resolution_note must not be empty");
			const actor = actorHeader.trim();
			if (!actor) {
				throw new ApiFailure(
					400,
					"invalid_argument",
					"invalid identifier: actor_id must not be empty",
				);
			}
			if (actor === SYSTEM_ACTOR) {
				throw new ApiFailure(
					400,
					"invalid_argument",
					"invalid identifier: actor_id must not be the reserved system actor",
				);
			}

			const result = resolveByOperator(alertId, actor, trimmedNote, Date.now());
			if (result === "not_found") throw notFound();
			if (result === "conflict") {
				throw new ApiFailure(
					409,
					"conflict",
					`conflict: alert "${alertId}" is not open`,
				);
			}
			return { data: summary(result), request_id: id };
		});
	}),

	http.all(`${BASE}/*`, () =>
		respond(() => {
			throw new ApiFailure(404, "not_found", "no such endpoint");
		}),
	),
];
