import { useEffect, useMemo, useRef } from "react";
import type { Alert } from "#/api/types";
import { Button } from "#/components/ui/button";
import { formatAge } from "#/lib/grid-tick";
import { gsap } from "#/lib/motion";
import {
	alertTitle,
	type FleetSite,
	STATUS_COLOR,
	type Status,
	severityStatus,
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
	const alertingIds = useMemo(
		() => new Set(openAlerts.map((a) => a.asset_id)),
		[openAlerts],
	);
	const typeOf = (assetId: string) =>
		site.assets.find((a) => a.assetId === assetId)?.assetType;

	return (
		<div
			className="cmd-site-card absolute left-4 bottom-4 w-[300px] bg-[rgba(7,13,19,0.93)] border border-border p-[13px] shadow-[0_24px_60px_rgba(0,0,0,0.6)]"
			ref={ref}
		>
			<div className="flex justify-between items-start gap-[10px]">
				<div>
					<div className="font-heading text-[17px] font-semibold">
						{site.name}
					</div>
					<div className="mono muted text-[10.5px]">
						{site.id} · {site.region}
					</div>
				</div>
				<Button
					variant="ghost"
					size="inline"
					className="text-[18px] leading-none px-0.5"
					onClick={onClose}
					aria-label="Close site"
				>
					×
				</Button>
			</div>
			<div className="grid grid-cols-3 gap-[10px] my-3">
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
			<div className="grid grid-cols-[1fr_auto_1fr_auto] gap-x-[10px] gap-y-[3px] text-[11.5px] mono">
				<span className="text-muted-foreground">Meters</span>
				<b>{site.meters.length}</b>
				<span className="text-muted-foreground">Assets</span>
				<b>{site.assets.length}</b>
				<span className="text-muted-foreground">Disconnected</span>
				<b>{site.disconnected}</b>
				<span className="text-muted-foreground">Operator</span>
				<b>{site.operatorId ?? "unassigned"}</b>
				<span className="text-muted-foreground">Last report</span>
				<b>
					{formatAge(
						site.lastSeenAt ? new Date(site.lastSeenAt).toISOString() : null,
						now,
					)}
				</b>
				<span className="text-muted-foreground">Status</span>
				<b style={{ color: STATUS_COLOR[status] }}>{status}</b>
			</div>
			{dark && (
				<p className="mt-[10px] text-[11.5px] text-warning leading-[1.45]">
					Telemetry is stale. Values show the last report before the site went
					dark.
				</p>
			)}
			<MeterCells meters={site.meters} alertingIds={alertingIds} />
			{openAlerts.length > 0 && (
				<div className="mt-[11px] flex flex-col gap-[5px]">
					{openAlerts.map((a) => (
						<button
							type="button"
							key={a.alert_id}
							className="flex items-center gap-[7px] text-left text-xs py-[6px] px-2 border border-(--edge) bg-[rgba(255,77,94,0.05)] hover:border-border"
							onClick={() => onOpenAlert(a)}
						>
							<StatusDot status={severityStatus(a.severity)} size={6} />
							{alertTitle(a, typeOf(a.asset_id))} · {a.asset_id}
						</button>
					))}
				</div>
			)}
		</div>
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
	return (
		<div>
			<div className="text-[11px] text-muted-foreground font-heading">
				{label}
			</div>
			<div className="text-[15px] mono">{value}</div>
			<div className="h-[3px] bg-white/[0.07] mt-[5px]">
				<i
					className="block h-full transition-[width] duration-500 ease-in-out"
					style={{
						width: `${Math.min(1, Math.max(0, pct)) * 100}%`,
						background: color,
					}}
				/>
			</div>
		</div>
	);
}
