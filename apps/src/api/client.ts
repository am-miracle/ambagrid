import { ACTOR_ID_HEADER } from "./contract";
import type {
	Alert,
	AlertDetail,
	AlertStatus,
	ApplyPaymentResult,
	Asset,
	AssetType,
	AuditEvent,
	CollectionBody,
	CustomerSummary,
	ErrorBody,
	ErrorCode,
	MeterCommand,
	ObjectBody,
	ReadingInterval,
	ReadingMetric,
	ReadingSeries,
	Severity,
	Site,
} from "./types";

const BASE_URL = import.meta.env.VITE_API_BASE_URL ?? "http://localhost:8081";

export class ApiError extends Error {
	constructor(
		readonly status: number,
		readonly code: ErrorCode,
		message: string,
		readonly requestId: string,
	) {
		super(message);
	}
}

type Query = Record<string, string | number | undefined>;

async function request<T>(
	path: string,
	query: Query = {},
	init: RequestInit = {},
): Promise<T> {
	const url = new URL(path, BASE_URL);
	for (const [key, value] of Object.entries(query)) {
		if (value !== undefined && value !== "") {
			url.searchParams.set(key, String(value));
		}
	}

	const response = await fetch(url, init);
	const body = await response.json().catch(() => null);
	if (!response.ok) {
		const error = (body as ErrorBody | null)?.error;
		throw new ApiError(
			response.status,
			error?.code ?? "internal",
			error?.message ?? response.statusText,
			error?.request_id ?? response.headers.get("X-Request-Id") ?? "",
		);
	}
	return body as T;
}

export interface Paging {
	limit?: number;
	cursor?: string;
}

export interface AssetFilter extends Paging {
	site_id?: string;
	asset_type?: AssetType;
}

export interface AlertFilter extends Paging {
	site_id?: string;
	asset_id?: string;
	status?: AlertStatus;
	severity?: Severity;
	kind?: string;
}

export interface ReadingsQuery {
	metric: ReadingMetric;
	interval: ReadingInterval;
	from: string;
	to: string;
}

export const api = {
	listSites: (paging: Paging = {}) =>
		request<CollectionBody<Site>>("/v1/sites", { ...paging }),

	listAssets: (filter: AssetFilter = {}) =>
		request<CollectionBody<Asset>>("/v1/assets", { ...filter }),

	getAsset: (assetId: string) =>
		request<ObjectBody<Asset>>(`/v1/assets/${encodeURIComponent(assetId)}`),

	getReadings: (assetId: string, query: ReadingsQuery) =>
		request<ObjectBody<ReadingSeries>>(
			`/v1/assets/${encodeURIComponent(assetId)}/readings`,
			{ ...query },
		),

	listAlerts: (filter: AlertFilter = {}) =>
		request<CollectionBody<Alert>>("/v1/alerts", { ...filter }),

	getAlert: (alertId: string) =>
		request<ObjectBody<AlertDetail>>(
			`/v1/alerts/${encodeURIComponent(alertId)}`,
		),

	resolveAlert: (alertId: string, actorId: string, resolutionNote: string) =>
		request<ObjectBody<Alert>>(
			`/v1/alerts/${encodeURIComponent(alertId)}/resolve`,
			{},
			{
				method: "POST",
				headers: {
					"Content-Type": "application/json",
					[ACTOR_ID_HEADER]: actorId,
				},
				body: JSON.stringify({ resolution_note: resolutionNote }),
			},
		),

	applyDevPayment: (
		customerId: string,
		amountMinorUnits: number,
		currency: string,
	) =>
		request<ObjectBody<ApplyPaymentResult>>(
			"/v1/dev/payments",
			{},
			{
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({
					customer_id: customerId,
					amount_minor_units: amountMinorUnits,
					currency,
				}),
			},
		),

	listDevCustomers: () =>
		request<CollectionBody<CustomerSummary>>("/v1/dev/customers"),

	listDevAuditEvents: (customerId?: string) =>
		request<CollectionBody<AuditEvent>>(
			"/v1/dev/audit-events",
			customerId ? { customer_id: customerId } : {},
		),

	listDevCommands: () =>
		request<CollectionBody<MeterCommand>>("/v1/dev/commands"),
};
