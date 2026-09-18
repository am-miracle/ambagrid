import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { ApiError } from "#/api/client";
import { ASSET_TYPE_LABEL, SYSTEM_ACTOR } from "#/api/contract";
import { useAlert, useResolveAlert } from "#/api/queries";
import type { Alert } from "#/api/types";
import { useActorId } from "#/lib/actor";
import { formatDuration } from "#/lib/format";
import { gsap } from "#/lib/motion";
import type { AssetTelemetry } from "#/lib/telemetry";
import {
	alertSummary,
	alertTitle,
	type FleetSite,
	formatClock,
	isOpenAt,
	openedAt,
	STATUS_COLOR,
	severityStatus,
	shortId,
} from "../model";
import { type Series, Sparkline, StatusDot } from "../ui";

interface Activity {
	id: string;
	at: number;
	actor: string;
	kind: "system" | "operator";
	text: string;
}

export function AlertDetail({
	alert,
	site,
	asset,
	at,
	series,
	related,
	onBack,
	onOpenRelated,
}: {
	alert: Alert;
	site: FleetSite | undefined;
	asset: AssetTelemetry | undefined;
	at: number;
	series: Series | null;
	related: Alert[];
	onBack: () => void;
	onOpenRelated: (a: Alert) => void;
}) {
	const ref = useRef<HTMLDivElement | null>(null);
	const { data: detail } = useAlert(alert.alert_id);

	// The parent keys this panel by alert, so each alert re-enters.
	useEffect(() => {
		gsap.fromTo(
			ref.current,
			{ x: 26, opacity: 0 },
			{ x: 0, opacity: 1, duration: 0.55, ease: "power3.out" },
		);
	}, []);

	const openNow = isOpenAt(alert, at);
	const opened: Activity = {
		id: "opened",
		at: openedAt(alert),
		actor: "engine",
		kind: "system",
		text: `Alert opened. ${alertSummary(alert)}.`,
	};
	const activity = [
		opened,
		...(detail?.resolutions ?? []).map((r): Activity => {
			const system = r.resolved_by === SYSTEM_ACTOR;
			return {
				id: r.resolution_id,
				at: Date.parse(r.resolved_at),
				actor: system ? "automatic recovery" : r.resolved_by,
				kind: system ? "system" : "operator",
				text: system
					? alertSummary({ ...alert, reason: r.resolution_note })
					: r.resolution_note,
			};
		}),
	]
		.filter((entry) => entry.at <= at)
		.sort((a, b) => b.at - a.at);

	return (
		<div className="detail" ref={ref}>
			<div className="panel-head">
				<button type="button" className="back" onClick={onBack}>
					← Inbox
				</button>
				<span className={`state ${openNow ? "open" : "resolved"}`}>
					{openNow ? "open" : "resolved"}
				</span>
			</div>

			<div className={`detail-hero sev-${alert.severity}`}>
				<div className="mono detail-id">
					{shortId(alert.alert_id)} · opened {formatClock(openedAt(alert))} ·{" "}
					{formatDuration(at - openedAt(alert))} ago
				</div>
				<h3>{alertTitle(alert, asset?.assetType)}</h3>
				<div className="detail-where mono">
					{site?.name ?? alert.site_id} · {alert.asset_id}
					{asset && ` · ${ASSET_TYPE_LABEL[asset.assetType]}`}
				</div>
				<p className="detail-summary">{alertSummary(alert)}</p>
			</div>

			<div className="detail-stats mono">
				<div>
					<span>Kind</span>
					<b>{alert.kind}</b>
				</div>
				<div>
					<span>Severity</span>
					<b style={{ color: STATUS_COLOR[severityStatus(alert.severity)] }}>
						{alert.severity}
					</b>
				</div>
				{asset?.reportedHouseholdId && (
					<div>
						<span>Household</span>
						<b>{asset.reportedHouseholdId}</b>
					</div>
				)}
				<div>
					<span>Source event</span>
					<b>{alert.source_event_id ?? "—"}</b>
				</div>
				<div>
					<span>Resolved by</span>
					<b>
						{alert.resolved_by === null
							? "—"
							: alert.resolved_by === SYSTEM_ACTOR
								? "automatic recovery"
								: alert.resolved_by}
					</b>
				</div>
			</div>

			{series && (
				<div className="detail-chart">
					<Sparkline
						series={series}
						now={at}
						height={112}
						color={STATUS_COLOR[severityStatus(alert.severity)]}
					/>
				</div>
			)}

			<div className="detail-links">
				<Link to="/assets/$assetId" params={{ assetId: alert.asset_id }}>
					Open asset telemetry →
				</Link>
			</div>

			{alert.status === "open" && (
				<ResolveForm key={alert.alert_id} alertId={alert.alert_id} />
			)}

			{alert.resolution_note && !isOpenAt(alert, at) && (
				<div className="resolution">
					<span className="mono">resolution</span>
					<b>
						{alert.resolved_by === SYSTEM_ACTOR
							? alertSummary({ ...alert, reason: alert.resolution_note })
							: alert.resolution_note}
					</b>
				</div>
			)}

			<div className="hist">
				<div className="rail-title">Activity</div>
				{activity.map((h) => (
					<div key={h.id} className={`hist-item ${h.kind}`}>
						<span className="mono hist-time">{formatClock(h.at)}</span>
						<div>
							<div className="hist-actor mono">{h.actor}</div>
							<div className="hist-text">{h.text}</div>
						</div>
					</div>
				))}
			</div>

			{related.length > 0 && (
				<div className="hist">
					<div className="rail-title">Same asset, earlier</div>
					{related.map((r) => (
						<button
							type="button"
							key={r.alert_id}
							className="related"
							onClick={() => onOpenRelated(r)}
						>
							<StatusDot status={severityStatus(r.severity)} size={6} />
							<span>{new Date(r.opened_at).toLocaleDateString()}</span>
							<span className="mono muted">
								{r.status === "open"
									? "open"
									: r.resolved_by === SYSTEM_ACTOR
										? "recovered"
										: (r.resolution_note ?? "resolved")}
							</span>
						</button>
					))}
				</div>
			)}
		</div>
	);
}

// Resolution is the one operator command the API exposes. Acknowledge and
// assign are not backend states yet, so they are not offered here.
function ResolveForm({ alertId }: { alertId: string }) {
	const actorId = useActorId();
	const resolve = useResolveAlert(alertId);
	const [note, setNote] = useState("");
	const error = resolve.error instanceof ApiError ? resolve.error : null;

	return (
		<form
			className="resolve-form"
			onSubmit={(e) => {
				e.preventDefault();
				if (actorId && note.trim())
					resolve.mutate({ actorId, note: note.trim() });
			}}
		>
			<label className="rail-title" htmlFor={`note-${alertId}`}>
				Resolve as {actorId || "…"}
			</label>
			<textarea
				id={`note-${alertId}`}
				value={note}
				onChange={(e) => setNote(e.target.value)}
				placeholder="What was done, e.g. fan cleaned"
				rows={2}
			/>
			{!actorId && (
				<p className="form-note">Set an operator id in the left rail first.</p>
			)}
			{error?.status === 409 && (
				<p className="form-note">
					Already resolved by someone else. The record above has refreshed.
				</p>
			)}
			{error && error.status !== 409 && (
				<p className="form-note">
					{error.message} (request {error.requestId})
				</p>
			)}
			<div className="actions">
				<button
					type="submit"
					className="resolve"
					disabled={resolve.isPending || !actorId || !note.trim()}
				>
					{resolve.isPending ? "Resolving…" : "Resolve"}
				</button>
			</div>
		</form>
	);
}
