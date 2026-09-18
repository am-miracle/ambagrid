import { PollingTelemetrySource } from "./polling-source";
import type { TelemetrySource } from "./types";

export { PollingTelemetrySource, toAssetTelemetry } from "./polling-source";
export type {
	AssetTelemetry,
	AssetType,
	SiteTelemetrySnapshot,
	TelemetrySource,
	TelemetrySubscriber,
} from "./types";

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL ?? "http://localhost:8081";

// Swap this line for an SseTelemetrySource once the API pushes; nothing that
// calls telemetrySource.subscribe() needs to change.
export const telemetrySource: TelemetrySource = new PollingTelemetrySource({
	baseUrl: apiBaseUrl,
});
