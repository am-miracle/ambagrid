import { RECOVERY_MARGIN_C, SYSTEM_ACTOR } from "#/api/contract";
import type { AlertDetail } from "#/api/types";
import {
	ASSETS,
	type AssetSpec,
	criticalFor,
	HEAT_EPISODES,
	lastReportAt,
	sample,
} from "./fleet";
import { seededUuid } from "./noise";

// Every alert the engine emits today is a critical internal_temperature alert
// (services/engine-rust/src/domain/rules.rs), so the mock emits nothing else.
const KIND = "internal_temperature";
const MINUTE = 60_000;

const iso = (t: number) => new Date(t).toISOString();
const openReason = (temperature: number, threshold: number) =>
	`internal_temperature_high:${temperature.toFixed(1)}C>=threshold:${threshold.toFixed(1)}C`;
const recoveredNote = (temperature: number, threshold: number) =>
	`internal_temperature_recovered:${temperature.toFixed(1)}C<threshold:${threshold.toFixed(1)}C`;

const specFor = (assetId: string) =>
	ASSETS.find((a) => a.asset_id === assetId) as AssetSpec;

function seedAlerts(): AlertDetail[] {
	return HEAT_EPISODES.map((episode, index) => {
		const spec = specFor(episode.asset_id);
		const threshold = criticalFor(spec);
		const openedAt = episode.start + 6 * MINUTE;
		const alert: AlertDetail = {
			alert_id: seededUuid(`${episode.asset_id}:${index}`),
			asset_id: spec.asset_id,
			site_id: spec.site_id,
			kind: KIND,
			severity: "critical",
			status: "open",
			reason: openReason(
				sample(spec, "internal_temperature", openedAt),
				threshold,
			),
			opened_at: iso(openedAt),
			source_event_id: `telemetry-evt-${spec.asset_id}-${openedAt / 1000}`,
			resolved_at: null,
			resolution_note: null,
			resolved_by: null,
			resolutions: [],
		};
		if (episode.end === null) return alert;

		const operator = episode.operatorResolution;
		const resolvedAt = operator
			? episode.end - 2 * MINUTE
			: episode.end + 5 * MINUTE;
		const note =
			operator?.note ??
			recoveredNote(
				sample(spec, "internal_temperature", resolvedAt),
				threshold - RECOVERY_MARGIN_C,
			);
		return resolve(alert, resolvedAt, note, operator?.actor ?? SYSTEM_ACTOR);
	});
}

function resolve(
	alert: AlertDetail,
	at: number,
	note: string,
	actor: string,
): AlertDetail {
	return {
		...alert,
		status: "resolved",
		resolved_at: iso(at),
		resolution_note: note,
		resolved_by: actor,
		resolutions: [
			{
				resolution_id: seededUuid(`${alert.alert_id}:${at}`),
				resolved_at: iso(at),
				resolution_note: note,
				resolved_by: actor,
				recorded_at: iso(at + 40),
			},
			...alert.resolutions,
		],
	};
}

let alerts = seedAlerts();

// Port of ThresholdPolicy.evaluate / recovered, applied to each asset's latest
// report. Run lazily on every request instead of on a timer.
export function evaluatePolicy(now: number) {
	for (const spec of ASSETS) {
		const reportedAt = lastReportAt(spec, now);
		const temperature = sample(spec, "internal_temperature", reportedAt);
		const threshold = criticalFor(spec);
		const open = alerts.find(
			(a) =>
				a.asset_id === spec.asset_id && a.kind === KIND && a.status === "open",
		);

		if (!open && temperature >= threshold) {
			const latest = alerts
				.filter((a) => a.asset_id === spec.asset_id)
				.map((a) => Date.parse(a.resolved_at ?? a.opened_at))
				.reduce((max, t) => Math.max(max, t), 0);
			// Only a report newer than the last resolution can reopen.
			if (reportedAt <= latest) continue;
			alerts.push({
				alert_id: seededUuid(`${spec.asset_id}:${reportedAt}`),
				asset_id: spec.asset_id,
				site_id: spec.site_id,
				kind: KIND,
				severity: "critical",
				status: "open",
				reason: openReason(temperature, threshold),
				opened_at: iso(reportedAt),
				source_event_id: `telemetry-evt-${spec.asset_id}-${reportedAt / 1000}`,
				resolved_at: null,
				resolution_note: null,
				resolved_by: null,
				resolutions: [],
			});
		} else if (open && temperature < threshold - RECOVERY_MARGIN_C) {
			replace(
				resolve(
					open,
					reportedAt,
					recoveredNote(temperature, threshold - RECOVERY_MARGIN_C),
					SYSTEM_ACTOR,
				),
			);
		}
	}
}

function replace(next: AlertDetail) {
	alerts = alerts.map((a) => (a.alert_id === next.alert_id ? next : a));
}

export const allAlerts = () => alerts;

export function resolveByOperator(
	alertId: string,
	actor: string,
	note: string,
	now: number,
): AlertDetail | "not_found" | "conflict" {
	const alert = alerts.find((a) => a.alert_id === alertId);
	if (!alert) return "not_found";
	if (alert.status !== "open") return "conflict";
	const next = resolve(alert, now, note, actor);
	replace(next);
	return next;
}
