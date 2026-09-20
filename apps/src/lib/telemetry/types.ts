export type AssetType = "smart_meter" | "battery_bms" | "solar_inverter";

export interface AssetTelemetry {
	assetId: string;
	assetType: AssetType;
	observedAt: string;
	internalTemperature: number | null;
	voltage?: number | null;
	current?: number | null;
	activePower?: number | null;
	frequency?: number | null;
	totalKwh?: number | null;
	relayClosed?: boolean | null;
	reportedHouseholdId?: string | null;
	batterySocPct?: number | null;
	solarIrradiance?: number | null;
}

export interface SiteTelemetrySnapshot {
	siteId: string;
	receivedAt: string;
	assets: AssetTelemetry[];
}

export interface TelemetrySubscriber {
	onSnapshot(snapshot: SiteTelemetrySnapshot): void;
	onError?(error: Error): void;
}

// The seam: swap the transport (poll today, SSE later) without touching
// anything that subscribes.
export interface TelemetrySource {
	subscribe(siteId: string, subscriber: TelemetrySubscriber): () => void;
}
