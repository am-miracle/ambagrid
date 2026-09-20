import { useEffect, useMemo, useRef } from "react";
import type { Alert } from "#/api/types";
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
	// The parent keys this card by site, so a new site remounts and re-enters.
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
		<div className="site-card" ref={ref}>
			<div className="site-head">
				<div>
					<div className="site-name">{site.name}</div>
					<div className="mono muted">
						{site.id} · {site.region}
					</div>
				</div>
				<button
					type="button"
					className="x"
					onClick={onClose}
					aria-label="Close site"
				>
					×
				</button>
			</div>
			<div className="flow">
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
			<div className="site-grid mono">
				<span>Meters</span>
				<b>{site.meters.length}</b>
				<span>Assets</span>
				<b>{site.assets.length}</b>
				<span>Disconnected</span>
				<b>{site.disconnected}</b>
				<span>Operator</span>
				<b>{site.operatorId ?? "unassigned"}</b>
				<span>Last report</span>
				<b>
					{formatAge(
						site.lastSeenAt ? new Date(site.lastSeenAt).toISOString() : null,
						now,
					)}
				</b>
				<span>Status</span>
				<b style={{ color: STATUS_COLOR[status] }}>{status}</b>
			</div>
			{dark && (
				<p className="site-note">
					Telemetry is stale. Values show the last report before the site went
					dark.
				</p>
			)}
			<MeterCells meters={site.meters} alertingIds={alertingIds} />
			{openAlerts.length > 0 && (
				<div className="site-alerts">
					{openAlerts.map((a) => (
						<button
							type="button"
							key={a.alert_id}
							className={`mini sev-${a.severity}`}
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
		<div className="flow-cell">
			<div className="flow-label">{label}</div>
			<div className="flow-value mono">{value}</div>
			<div className="flow-bar">
				<i
					style={{
						width: `${Math.min(1, Math.max(0, pct)) * 100}%`,
						background: color,
					}}
				/>
			</div>
		</div>
	);
}
