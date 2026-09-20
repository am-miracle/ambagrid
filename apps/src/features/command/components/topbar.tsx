import type { ReactNode } from "react";
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

export function Topbar({
	kpi,
	at,
	live,
	openCount,
	onToggleRail,
	onToggleInbox,
}: {
	kpi: FleetKpis;
	at: number;
	live: boolean;
	openCount: number;
	onToggleRail: () => void;
	onToggleInbox: () => void;
}) {
	return (
		<header className="topbar panel-anim">
			<button
				type="button"
				className="rail-toggle"
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
			<div className="brand">
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
					<div className="brand-name">AmbaGrid</div>
					<div className="brand-sub mono">Fleet control</div>
				</div>
			</div>

			<div className="kpis">
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

			<div className="topbar-right">
				<div className="clock mono">
					<span className={`live${live ? " on" : ""}`} />
					{live ? "LIVE" : "REPLAY"} {formatClock(at)} {timeZoneLabel}
				</div>
				<button
					type="button"
					className="ghost inbox-toggle"
					onClick={onToggleInbox}
				>
					Alerts <span className="badge">{openCount}</span>
				</button>
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
		<div className={`kpi tone-${tone}`}>
			<div className="kpi-label">{label}</div>
			<div className="kpi-value mono">
				{value}
				{sub && <em>{sub}</em>}
			</div>
			{bar !== undefined && (
				<div className="kpi-bar">
					<i
						style={{ width: `${bar * 100}%`, background: STATUS_COLOR[tone] }}
					/>
				</div>
			)}
		</div>
	);
}
