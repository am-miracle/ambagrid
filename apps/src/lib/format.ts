import { METRIC_LABEL } from "#/api/contract";
import type { ReadingMetric } from "#/api/types";

const compact = new Intl.NumberFormat("en", {
	notation: "compact",
	maximumFractionDigits: 1,
});

export const formatCompact = (value: number) => compact.format(value);

export function formatValue(
	value: number | null | undefined,
	digits = 1,
): string {
	if (value === null || value === undefined) return "—";
	return value.toLocaleString("en", {
		minimumFractionDigits: digits,
		maximumFractionDigits: digits,
	});
}

export const METRIC_DIGITS: Record<ReadingMetric, number> = {
	internal_temperature: 1,
	voltage: 1,
	current: 2,
	active_power: 2,
	frequency: 2,
	total_kwh: 1,
	battery_soc_pct: 0,
	solar_irradiance: 0,
};

export const formatMetric = (
	metric: ReadingMetric,
	value: number | null | undefined,
) =>
	`${formatValue(value, METRIC_DIGITS[metric])} ${METRIC_LABEL[metric].unit}`;

const dateTime = new Intl.DateTimeFormat("en-GB", {
	day: "2-digit",
	month: "short",
	hour: "2-digit",
	minute: "2-digit",
});

export const formatDateTime = (iso: string) => dateTime.format(new Date(iso));

export function formatDuration(ms: number): string {
	const minutes = Math.round(ms / 60_000);
	if (minutes < 60) return `${minutes}m`;
	const hours = Math.floor(minutes / 60);
	if (hours < 48) return `${hours}h ${minutes % 60}m`;
	return `${Math.floor(hours / 24)}d ${hours % 24}h`;
}

// Engine reasons are machine-shaped, e.g.
// internal_temperature_high:72.4C>=threshold:70.0C
export function describeReason(reason: string): string {
	const high = reason.match(
		/^internal_temperature_high:([\d.]+)C>=threshold:([\d.]+)C$/,
	);
	if (high)
		return `Internal temperature ${high[1]}°C reached the ${high[2]}°C limit`;
	const recovered = reason.match(
		/^internal_temperature_recovered:([\d.]+)C<threshold:([\d.]+)C$/,
	);
	if (recovered) {
		return `Recovered to ${recovered[1]}°C, below the ${recovered[2]}°C recovery line`;
	}
	return reason;
}

export const humanizeKind = (kind: string) =>
	kind.replaceAll("_", " ").replace(/^\w/, (c) => c.toUpperCase());
