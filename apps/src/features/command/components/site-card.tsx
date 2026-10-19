import { useEffect, useMemo, useRef } from "react";
import type { Alert } from "#/api/types";
import { Button } from "#/components/ui/button";
import { formatAge } from "#/lib/grid-tick";
import { gsap } from "#/lib/motion";
import {
	alertTitle,
	type FleetSite,
	healthStatusTone,
	STATUS_COLOR,
	type Status,
	severityStatus,
	toIso,
} from "../model";
import { StatusDot } from "../ui";
import { MeterCells } from "./meter-cells";

export function SiteCard({
	site,
	status,
	openAlerts,
	now,
	onOpenAlert,
	onClose,
}: {
	site: FleetSite;
	status: Status;
	openAlerts: Alert[];
	now: number;
	onOpenAlert: (a: Alert) => void;
	onClose: () => void;
}) {
	const ref = useRef<HTMLDivElement | null>(null);
	useEffect(() => {
		gsap.fromTo(
			ref.current,
			{ x: -16, opacity: 0 },
			{ x: 0, opacity: 1, duration: 0.5, ease: "power3.out" },
		);
	}, []);

	const dark = status === "offline";
	const healthTone = healthStatusTone(site.healthStatus);
	const alertingIds = useMemo(
		() => new Set(openAlerts.map((a) => a.asset_id)),
		[openAlerts],
	);
	const typeOf = (assetId: string) =>
		site.assets.find((a) => a.assetId === assetId)?.assetType;

	return (
		<div
			className="cmd-site-card absolute left-4 bottom-4 w-75 bg-[rgba(7,13,19,0.93)] border border-border rounded-lg p-3.25 shadow-[0_24px_60px_rgba(0,0,0,0.6)] backdrop-blur-sm overflow-y-auto max-h-[calc(100%-2rem)]"
			ref={ref}
		>
			<div className="flex justify-between items-start gap-2.5">
				<div className="flex items-start gap-2">
					<StatusDot status={status} size={8} className="mt-1.5 flex-none" />
					<div>
						<div className="font-heading text-[17px] font-semibold leading-tight">
							{site.name}
						</div>
						<div className="mono muted text-[10.5px] mt-0.5">
							{site.id} · {site.region}
						</div>
					</div>
				</div>
				<Button
					variant="ghost"
					size="inline"
					className="text-muted-foreground hover:text-foreground text-[18px] leading-none px-0.5 -mt-0.5"
					onClick={onClose}
					aria-label="Close site"
				>
					×
				</Button>
			</div>

			{dark && (
				<div className="mt-2.5 flex items-center gap-1.5 text-[11.5px] text-warning leading-[1.45] bg-warning/8 border border-warning/20 rounded px-2 py-1.5">
					<span className="flex-none text-[13px]" aria-hidden="true">
						⚠
					</span>
					Telemetry is stale — values show the last report before the site went
					dark.
				</div>
			)}

			<div className="grid grid-cols-3 gap-2.5 mt-3">
				<FlowCell
					label="Solar"
					value={
						site.irradiance === null
							? "—"
							: `${Math.round(site.irradiance)} W/m²`
					}
					pct={site.solarRatio ?? 0}
					color={STATUS_COLOR.healthy}
				/>
				<FlowCell
					label="Battery"
					value={site.soc === null ? "—" : `${Math.round(site.soc * 100)}%`}
					pct={site.soc ?? 0}
					color={
						site.soc !== null && site.soc < 0.35
							? STATUS_COLOR.warning
							: STATUS_COLOR.healthy
					}
				/>
				<FlowCell
					label="Load"
					value={`${site.loadKw.toFixed(1)} kW`}
					pct={site.loadRatio}
					color="#8FF6FF"
				/>
			</div>

			<MetaSection label="Site">
				<MetaRow label="Status" value={status} color={STATUS_COLOR[status]} />
				<MetaRow label="Meters" value={site.meters.length} />
				<MetaRow label="Assets" value={site.assets.length} />
				<MetaRow
					label="Disconnected"
					value={site.disconnected}
					warn={site.disconnected > 0}
				/>
				<MetaRow
					label="Operator"
					value={site.operatorId ?? "unassigned"}
					dim={!site.operatorId}
				/>
			</MetaSection>

			<MetaSection label="Connectivity">
				<MetaRow
					label="Last report"
					value={formatAge(toIso(site.lastSeenAt), now)}
				/>
				<MetaRow
					label="Gateway contact"
					value={formatAge(toIso(site.lastContactAt), now)}
				/>
				<MetaRow
					label="Gateway"
					value={site.healthStatus}
					color={STATUS_COLOR[healthTone]}
				/>
			</MetaSection>

			<MetaSection label="Queue">
				<MetaRow
					label="Pending"
					value={site.queueDepth}
					warn={site.queueDepth > 0}
				/>
				<MetaRow
					label="Oldest pending"
					value={formatAge(toIso(site.oldestPendingAt), now)}
				/>
				<MetaRow
					label="Last sync"
					value={formatAge(toIso(site.lastSuccessfulSyncAt), now)}
				/>
				<MetaRow
					label="Last failure"
					value={site.lastFailedSyncReason ?? "—"}
					title={site.lastFailedSyncReason ?? undefined}
					truncate
					warn={!!site.lastFailedSyncReason}
				/>
			</MetaSection>

			<MeterCells meters={site.meters} alertingIds={alertingIds} />

			{openAlerts.length > 0 && (
				<div className="mt-3 flex flex-col gap-1.5">
					<div className="text-[10px] text-muted-foreground/70 font-heading tracking-[0.5px] uppercase">
						Open alerts
					</div>
					{openAlerts.map((a) => {
						const alertStatus = severityStatus(a.severity);
						return (
							<button
								type="button"
								key={a.alert_id}
								className="flex items-center gap-2 text-left text-xs py-1.5 px-2 rounded border border-border/60 hover:border-border hover:bg-white/3 transition-colors"
								style={{
									borderLeftWidth: 2,
									borderLeftColor: STATUS_COLOR[alertStatus],
								}}
								onClick={() => onOpenAlert(a)}
							>
								<StatusDot status={alertStatus} size={6} />
								<span className="flex-1 truncate">
									{alertTitle(a, typeOf(a.asset_id))}
								</span>
								<span className="mono text-muted-foreground text-[10px] flex-none">
									{a.asset_id.slice(0, 8)}
								</span>
							</button>
						);
					})}
				</div>
			)}
		</div>
	);
}

function MetaSection({
	label,
	children,
}: {
	label: string;
	children: React.ReactNode;
}) {
	return (
		<div className="mt-3 pt-2.5 border-t border-white/6">
			<div className="text-[10px] text-muted-foreground/70 font-heading tracking-[0.5px] uppercase mb-1.5">
				{label}
			</div>
			<div className="grid grid-cols-[1fr_auto] gap-x-2.5 gap-y-0.75 text-[11.5px] mono">
				{children}
			</div>
		</div>
	);
}

function MetaRow({
	label,
	value,
	color,
	warn,
	dim,
	title,
	truncate,
}: {
	label: string;
	value: string | number;
	color?: string;
	warn?: boolean;
	dim?: boolean;
	title?: string;
	truncate?: boolean;
}) {
	return (
		<>
			<span className="text-muted-foreground">{label}</span>
			<b
				className={`${truncate ? "truncate max-w-30" : ""} ${dim ? "text-muted-foreground/60" : ""}`}
				style={
					color ? { color } : warn ? { color: STATUS_COLOR.warning } : undefined
				}
				title={title}
			>
				{value}
			</b>
		</>
	);
}

function FlowCell({
	label,
	value,
	pct,
	color,
}: {
	label: string;
	value: string;
	pct: number;
	color: string;
}) {
	const clamped = Math.min(1, Math.max(0, pct)) * 100;
	return (
		<div>
			<div className="text-[10px] text-muted-foreground/70 font-heading tracking-[0.3px] uppercase">
				{label}
			</div>
			<div className="text-[15px] mono mt-0.5">{value}</div>
			<div className="h-1 bg-white/[0.07] mt-1.5 rounded-full overflow-hidden">
				<div
					className="h-full rounded-full transition-[width] duration-500 ease-in-out"
					style={{
						width: `${clamped}%`,
						background: color,
						boxShadow: clamped > 0 ? `0 0 6px ${color}40` : undefined,
					}}
				/>
			</div>
		</div>
	);
}
