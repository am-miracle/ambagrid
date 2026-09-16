import {
	PollingTelemetrySource,
	type TelemetrySource,
} from "./telemetry-source";

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL ?? "http://localhost:8081";

export const telemetrySource: TelemetrySource = new PollingTelemetrySource({
	baseUrl: apiBaseUrl,
});
