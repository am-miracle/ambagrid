import { Link } from "@tanstack/react-router";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { ApiError } from "#/api/client";
import { ASSET_TYPE_LABEL, SYSTEM_ACTOR } from "#/api/contract";
import { useAlert, useResolveAlert } from "#/api/queries";
import type { Alert } from "#/api/types";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import { Label } from "#/components/ui/label";
import { Textarea } from "#/components/ui/textarea";
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
import {
	PanelHeader,
	SectionTitle,
	type Series,
	Sparkline,
	StatusDot,
} from "../ui";

interface Activity {
	id: string;
	at: number;
	actor: string;
	kind: "system" | "operator";
	text: string;
}

const STATE_BADGE = { open: "critical", resolved: "success" } as const;

const SEV_HERO: Record<
	string,
	{ borderLeftColor: string; background: string }
> = {
	critical: {
		borderLeftColor: "var(--critical)",
		background: "linear-gradient(90deg, rgba(255,77,94,0.09), transparent 70%)",
	},
	warning: {
		borderLeftColor: "var(--warning)",
		background:
			"linear-gradient(90deg, rgba(245,165,36,0.08), transparent 70%)",
	},
};

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

	const stateKey = openNow ? "open" : "resolved";

	return (
		<div className="overflow-y-auto min-h-0" ref={ref}>
			<PanelHeader>
				<Button
					variant="link"
					size="inline"
					className="flex-1 justify-start"
					onClick={onBack}
				>
					← Inbox
				</Button>
				<Badge variant={STATE_BADGE[stateKey]}>{stateKey}</Badge>
			</PanelHeader>

			<div
				className="py-3.5 px-4 border-l-[3px] border-l-transparent"
				style={SEV_HERO[alert.severity]}
			>
				<div className="mono text-[10.5px] text-muted-foreground">
					{shortId(alert.alert_id)} · opened {formatClock(openedAt(alert))} ·{" "}
					{formatDuration(at - openedAt(alert))} ago
				</div>
				<h3 className="font-heading text-[21px] font-semibold my-1.5 mb-1 leading-[1.15]">
					{alertTitle(alert, asset?.assetType)}
				</h3>
				<div className="text-[11px] text-primary mono">
					{site?.name ?? alert.site_id} · {alert.asset_id}
					{asset && ` · ${ASSET_TYPE_LABEL[asset.assetType]}`}
				</div>
				<p className="text-[13px] leading-[1.55] text-[#b9cbd9] mt-2.5">
					{alertSummary(alert)}
				</p>
			</div>

			<div className="px-4 my-3.5 flex flex-col gap-1.5 mono">
				<DetailRow label="Kind">{alert.kind}</DetailRow>
				<DetailRow
					label="Severity"
					color={STATUS_COLOR[severityStatus(alert.severity)]}
				>
					{alert.severity}
				</DetailRow>
				{asset?.reportedHouseholdId && (
					<DetailRow label="Household">{asset.reportedHouseholdId}</DetailRow>
				)}
				<DetailRow label="Source event">
					{alert.source_event_id ?? "—"}
				</DetailRow>
				<DetailRow label="Resolved by">
					{alert.resolved_by === null
						? "—"
						: alert.resolved_by === SYSTEM_ACTOR
							? "automatic recovery"
							: alert.resolved_by}
				</DetailRow>
			</div>

			{series && (
				<div className="px-4 pb-2">
					<Sparkline
						series={series}
						now={at}
						height={112}
						color={STATUS_COLOR[severityStatus(alert.severity)]}
					/>
				</div>
			)}

			<div className="px-4 pb-3 text-xs">
				<Link
					className="text-primary no-underline hover:underline"
					to="/assets/$assetId"
					params={{ assetId: alert.asset_id }}
				>
					Open asset telemetry →
				</Link>
			</div>

			{alert.status === "open" && (
				<ResolveForm key={alert.alert_id} alertId={alert.alert_id} />
			)}

			{alert.resolution_note && !isOpenAt(alert, at) && (
				<div className="mx-4 mb-3.5 py-2.25 px-3 border border-[rgba(57,215,192,0.35)] bg-[rgba(57,215,192,0.07)] flex gap-2.5 items-baseline">
					<span className="mono text-[10px] text-success">resolution</span>
					<b className="text-[13.5px] font-medium">
						{alert.resolved_by === SYSTEM_ACTOR
							? alertSummary({ ...alert, reason: alert.resolution_note })
							: alert.resolution_note}
					</b>
				</div>
			)}

			<div className="px-4 pb-4">
				<SectionTitle>Activity</SectionTitle>
				{activity.map((h) => (
					<div
						key={h.id}
						className="flex gap-2.5 py-2 border-t border-[rgba(122,186,212,0.07)]"
					>
						<span className="mono text-[10.5px] text-muted-foreground pt-0.5 shrink-0 w-9.5">
							{formatClock(h.at)}
						</span>
						<div>
							<div
								className={`mono text-[10.5px] ${h.kind === "system" ? "text-muted-foreground" : "text-primary"}`}
							>
								{h.actor}
							</div>
							<div className="text-[12.5px] leading-normal text-[#b9cbd9] mt-0.5">
								{h.text}
							</div>
						</div>
					</div>
				))}
			</div>

			{related.length > 0 && (
				<div className="px-4 pb-4">
					<SectionTitle>Same asset, earlier</SectionTitle>
					{related.map((r) => (
						<button
							type="button"
							key={r.alert_id}
							className="group flex gap-2 items-center w-full text-left py-2 border-t border-[rgba(122,186,212,0.07)] text-[12.5px]"
							onClick={() => onOpenRelated(r)}
						>
							<StatusDot status={severityStatus(r.severity)} size={6} />
							<span className="group-hover:text-primary">
								{new Date(r.opened_at).toLocaleDateString()}
							</span>
							<span className="mono muted ml-auto text-[11px]">
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

function ResolveForm({ alertId }: { alertId: string }) {
	const actorId = useActorId();
	const resolve = useResolveAlert(alertId);
	const [note, setNote] = useState("");
	const error = resolve.error instanceof ApiError ? resolve.error : null;

	return (
		<form
			className="px-4 pb-3.5 flex flex-col gap-1.5"
			onSubmit={(e) => {
				e.preventDefault();
				if (actorId && note.trim())
					resolve.mutate({ actorId, note: note.trim() });
			}}
		>
			<Label className="mb-2" htmlFor={`note-${alertId}`}>
				Resolve as {actorId || "…"}
			</Label>
			<Textarea
				id={`note-${alertId}`}
				value={note}
				onChange={(e) => setNote(e.target.value)}
				placeholder="What was done, e.g. fan cleaned"
				rows={2}
			/>
			{!actorId && (
				<FormWarning>Set an operator id in the left rail first.</FormWarning>
			)}
			{error?.status === 409 && (
				<FormWarning>
					Already resolved by someone else. The record above has refreshed.
				</FormWarning>
			)}
			{error && error.status !== 409 && (
				<FormWarning>
					{error.message} (request {error.requestId})
				</FormWarning>
			)}
			<Button
				type="submit"
				variant="success"
				size="lg"
				disabled={resolve.isPending || !actorId || !note.trim()}
			>
				{resolve.isPending ? "Resolving…" : "Resolve"}
			</Button>
		</form>
	);
}

function DetailRow({
	label,
	color,
	children,
}: {
	label: string;
	color?: string;
	children: ReactNode;
}) {
	return (
		<div className="flex justify-between gap-3.5 text-[11.5px] border-b border-[rgba(122,186,212,0.07)] pb-1.25">
			<span className="text-muted-foreground">{label}</span>
			<b className="font-medium text-right" style={{ color }}>
				{children}
			</b>
		</div>
	);
}

function FormWarning({ children }: { children: ReactNode }) {
	return (
		<p className="m-0 text-[11.5px] text-warning leading-[1.45]">{children}</p>
	);
}
