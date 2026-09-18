import { useMemo } from "react";
import {
	CRITICAL_TEMPERATURE_C,
	INTERVAL_MAX_WINDOW_MS,
	METRIC_LABEL,
} from "#/api/contract";
import { useReadings } from "#/api/queries";
import type { Alert, AssetType, ReadingInterval } from "#/api/types";
import { openedAt, resolvedAt } from "./model";
import type { Series } from "./ui";

const LEAD_MS = 2 * 60 * 60 * 1000;
const TAIL_MS = 30 * 60 * 1000;
const INTERVALS: ReadingInterval[] = ["1m", "5m", "15m", "1h"];

// Only internal_temperature alerts map to a metric the API can chart; other
// kinds render without a series rather than a guessed one.
export function useAlertSeries(
	alert: Alert | null,
	assetType: AssetType | undefined,
	now: number,
): Series | null {
	const charted = alert?.kind === "internal_temperature" && assetType;
	// Round to the minute so the query key is stable between 1s clock ticks.
	const minute = Math.floor(now / 60_000) * 60_000;

	const query = useMemo(() => {
		if (!alert || !charted) return null;
		const from = openedAt(alert) - LEAD_MS;
		const resolved = resolvedAt(alert);
		const to = Math.min(
			minute,
			resolved === null ? minute : resolved + TAIL_MS,
		);
		const span = Math.max(60_000, to - from);
		const interval =
			INTERVALS.find((i) => INTERVAL_MAX_WINDOW_MS[i] >= span) ?? "1h";
		const cappedFrom = Math.max(from, to - INTERVAL_MAX_WINDOW_MS[interval]);
		return {
			metric: "internal_temperature" as const,
			interval,
			from: new Date(cappedFrom).toISOString(),
			to: new Date(to).toISOString(),
		};
	}, [alert, charted, minute]);

	const { data } = useReadings(alert?.asset_id ?? null, query);

	return useMemo(() => {
		if (!data || !assetType || data.asset_id !== alert?.asset_id) return null;
		return {
			label: METRIC_LABEL.internal_temperature.label,
			unit: "°C",
			threshold: CRITICAL_TEMPERATURE_C[assetType],
			points: data.points.map((p) => [Date.parse(p.time), p.value]),
		};
	}, [data, assetType, alert?.asset_id]);
}
