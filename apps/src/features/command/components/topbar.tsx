import type { ReactNode } from "react";
import { Badge } from "#/components/ui/badge";
import { Button } from "#/components/ui/button";
import {
	formatClock,
	STATUS_COLOR,
	type Status,
	timeZoneLabel,
} from "../model";
import { AnimatedNumber } from "../ui";

export interface FleetKpis {
	health: number;
	critical: number;
	offlineAssets: number;
	reporting: number;
	siteCount: number;
	disconnected: number;
}

const TONE_BORDER: Record<Status, string> = {
	critical: "var(--critical)",
	warning: "var(--warning)",
	offline: "var(--offline)",
	healthy: "var(--healthy)",
};

export function Topbar({
	kpi,
	at,
	live,
	openCount,
	onToggleRail,
	onToggleInbox,
	onOpenPayment,
}: {
	kpi: FleetKpis;
	at: number;
	live: boolean;
	openCount: number;
	onToggleRail: () => void;
	onToggleInbox: () => void;
	onOpenPayment: () => void;
}) {
	return (
		<header className="cmd-topbar col-span-full flex items-center gap-5 px-4.5 border-b border-(--edge) bg-linear-to-b from-[rgba(10,18,26,0.96)] to-[rgba(6,11,16,0.86)] backdrop-blur-sm z-30 panel-anim">
			<button
				type="button"
				className="cmd-rail-toggle hidden"
				onClick={onToggleRail}
				aria-label="Toggle filters"
			>
				<svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
					<path
						d="M1 3h14M4 8h8M6 13h4"
						stroke="currentColor"
						strokeWidth="1.6"
						strokeLinecap="round"
					/>
				</svg>
			</button>
			<div className="flex items-center gap-2.5 shrink-0">
				<svg width="26" height="26" viewBox="0 0 26 26" aria-hidden="true">
					<path
						d="M13 2 23 8v10L13 24 3 18V8z"
						fill="none"
						stroke="#39D7C0"
						strokeWidth="1.3"
						opacity="0.75"
					/>
					<path
						d="M13 7.5 18.5 11v6L13 20.5 7.5 17v-6z"
						fill="none"
						stroke="#8FF6FF"
						strokeWidth="1.2"
					/>
					<circle cx="13" cy="14" r="2.4" fill="#8FF6FF" />
				</svg>
				<div>
					<div className="font-heading text-[19px] font-bold tracking-[0.4px] leading-none">
						AmbaGrid
					</div>
					<div className="cmd-brand-sub text-[10.5px] text-muted-foreground mt-0.75 mono">
						Fleet control
					</div>
				</div>
			</div>

			<div className="flex gap-2 flex-1 min-w-0 overflow-x-auto scrollbar:none">
				<Kpi
					label="Fleet health"
					value={
						<>
							<AnimatedNumber value={kpi.health} decimals={1} />%
						</>
					}
					tone={
						kpi.health > 90
							? "healthy"
							: kpi.health > 78
								? "warning"
								: "critical"
					}
					bar={kpi.health / 100}
				/>
				<Kpi
					label="Critical alerts"
					value={<AnimatedNumber value={kpi.critical} />}
					tone={kpi.critical ? "critical" : "healthy"}
				/>
				<Kpi
					label="Offline assets"
					value={<AnimatedNumber value={kpi.offlineAssets} />}
					tone={kpi.offlineAssets ? "offline" : "healthy"}
				/>
				<Kpi
					label="Sites reporting"
					value={
						<>
							<AnimatedNumber value={kpi.reporting} />/{kpi.siteCount}
						</>
					}
					tone={kpi.reporting === kpi.siteCount ? "healthy" : "warning"}
				/>
				<Kpi
					label="Meters disconnected"
					value={<AnimatedNumber value={kpi.disconnected} />}
					tone={kpi.disconnected ? "warning" : "healthy"}
					sub="now"
				/>
			</div>

			<div className="flex items-center gap-3 shrink-0">
				<div className="cmd-clock text-xs text-muted-foreground flex items-center gap-1.75 mono">
					<span
						className={`w-1.75 h-1.75 rounded-full ${live ? "bg-success animate-[blip_2s_infinite]" : "bg-warning"}`}
					/>
					{live ? "LIVE" : "REPLAY"} {formatClock(at)} {timeZoneLabel}
				</div>
				<Button onClick={onOpenPayment}>Test payment</Button>
				<Button className="cmd-inbox-toggle hidden" onClick={onToggleInbox}>
					Alerts{" "}
					<Badge variant="count" className="ml-1.5">
						{openCount}
					</Badge>
				</Button>
			</div>
		</header>
	);
}

function Kpi({
	label,
	value,
	tone,
	sub,
	bar,
}: {
	label: string;
	value: ReactNode;
	tone: Status;
	sub?: string;
	bar?: number;
}) {
	return (
		<div
			className="cmd-kpi min-w-30.5 py-1.5 px-3 border-l-2 shrink-0"
			style={{ borderLeftColor: TONE_BORDER[tone] }}
		>
			<div className="font-heading text-[11.5px] text-muted-foreground tracking-[0.3px]">
				{label}
			</div>
			<div
				className="cmd-kpi-value text-[19px] font-medium leading-tight mono"
				style={tone === "critical" ? { color: "#ff8b96" } : undefined}
			>
				{value}
				{sub && (
					<em className="not-italic text-[10px] text-muted-foreground ml-1.25">
						{sub}
					</em>
				)}
			</div>
			{bar !== undefined && (
				<div className="h-0.5 bg-white/8 mt-1.25">
					<i
						className="block h-full transition-[width] duration-600 ease-in-out"
						style={{ width: `${bar * 100}%`, background: STATUS_COLOR[tone] }}
					/>
				</div>
			)}
		</div>
	);
}
