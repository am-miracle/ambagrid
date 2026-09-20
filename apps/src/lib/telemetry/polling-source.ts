import type {
	AssetTelemetry,
	AssetType,
	TelemetrySource,
	TelemetrySubscriber,
} from "./types";

interface AssetWireDTO {
	asset_id: string;
	asset_type: AssetType;
	internal_temperature: number | null;
	last_seen_at: string;
	smart_meter?: {
		voltage: number | null;
		current: number | null;
		active_power: number | null;
		frequency: number | null;
		total_kwh: number | null;
		relay_closed: boolean | null;
		reported_household_id: string | null;
	};
	battery_bms?: { battery_soc_pct: number | null };
	solar_inverter?: { solar_irradiance: number | null };
}

interface AssetPageWireDTO {
	data: AssetWireDTO[];
	page: { next_cursor: string };
}

export interface PollingTelemetrySourceOptions {
	baseUrl: string;
	intervalMs?: number;
	fetch?: typeof fetch;
}

const defaultIntervalMs = 5_000;
const pageSize = 200;

export class PollingTelemetrySource implements TelemetrySource {
	private readonly baseUrl: string;
	private readonly intervalMs: number;
	private readonly fetch: typeof fetch;

	constructor(options: PollingTelemetrySourceOptions) {
		this.baseUrl = options.baseUrl.replace(/\/$/, "");
		this.intervalMs = options.intervalMs ?? defaultIntervalMs;
		this.fetch = options.fetch ?? globalThis.fetch.bind(globalThis);

		if (this.intervalMs <= 0) {
			throw new Error("telemetry polling interval must be greater than zero");
		}
	}

	subscribe(siteId: string, subscriber: TelemetrySubscriber): () => void {
		let active = true;
		let timer: ReturnType<typeof setTimeout> | undefined;
		let request: AbortController | undefined;

		const poll = async () => {
			request = new AbortController();
			try {
				const assets = await this.loadSite(siteId, request.signal);
				if (active) {
					subscriber.onSnapshot({
						siteId,
						receivedAt: new Date().toISOString(),
						assets,
					});
				}
			} catch (error) {
				if (active) {
					subscriber.onError?.(asError(error));
				}
			} finally {
				if (active) {
					timer = setTimeout(poll, this.intervalMs);
				}
			}
		};

		void poll();

		return () => {
			active = false;
			if (timer !== undefined) clearTimeout(timer);
			request?.abort();
		};
	}

	private async loadSite(
		siteId: string,
		signal: AbortSignal,
	): Promise<AssetTelemetry[]> {
		const assets: AssetTelemetry[] = [];
		const seenCursors = new Set<string>();
		let cursor = "";

		do {
			const query = new URLSearchParams({
				site_id: siteId,
				limit: String(pageSize),
			});
			if (cursor) query.set("cursor", cursor);

			const response = await this.fetch(`${this.baseUrl}/v1/assets?${query}`, {
				signal,
			});
			if (!response.ok) {
				throw new Error(
					`telemetry request failed with status ${response.status}`,
				);
			}

			const page = (await response.json()) as AssetPageWireDTO;
			assets.push(...page.data.map(toAssetTelemetry));
			cursor = page.page.next_cursor;

			if (cursor && seenCursors.has(cursor)) {
				throw new Error("telemetry pagination returned a repeated cursor");
			}
			seenCursors.add(cursor);
		} while (cursor);

		return assets;
	}
}

export function toAssetTelemetry(asset: AssetWireDTO): AssetTelemetry {
	return {
		assetId: asset.asset_id,
		assetType: asset.asset_type,
		observedAt: asset.last_seen_at,
		internalTemperature: asset.internal_temperature,
		voltage: asset.smart_meter?.voltage,
		current: asset.smart_meter?.current,
		activePower: asset.smart_meter?.active_power,
		frequency: asset.smart_meter?.frequency,
		totalKwh: asset.smart_meter?.total_kwh,
		relayClosed: asset.smart_meter?.relay_closed,
		reportedHouseholdId: asset.smart_meter?.reported_household_id,
		batterySocPct: asset.battery_bms?.battery_soc_pct,
		solarIrradiance: asset.solar_inverter?.solar_irradiance,
	};
}

function asError(error: unknown): Error {
	return error instanceof Error ? error : new Error("unknown telemetry error");
}
