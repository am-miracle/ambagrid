import type { Alert, AssetType } from "#/api/types";
import { formatDuration } from "#/lib/format";
import { LIVE_TICK_MS } from "#/lib/grid-tick";
import { alertTitle, type FleetSite, openedAt } from "../model";

const SEV_BAR_STYLE: Record<string, React.CSSProperties> = {
	critical: {
		background: "var(--critical)",
		boxShadow: "0 0 10px var(--critical)",
	},
	warning: { background: "var(--warning)" },
};

const SEV_STATE_STYLE: Record<string, { color: string; borderColor: string }> =
	{
		critical: { color: "#ff8b96", borderColor: "rgba(255,77,94,0.4)" },
		warning: { color: "var(--warning)", borderColor: "rgba(245,165,36,0.4)" },
		info: { color: "var(--cyan)", borderColor: "rgba(143,246,255,0.35)" },
	};

export function AlertInbox({
	alerts,
	at,
	sitesById,
	assetTypeOf,
	onOpen,
	onClose,
}: {
	alerts: Alert[];
	at: number;
	sitesById: Map<string, FleetSite>;
	assetTypeOf: (assetId: string) => AssetType | undefined;
	onOpen: (a: Alert) => void;
	onClose: () => void;
}) {
	const critical = alerts.filter((a) => a.severity === "critical").length;
	return (
		<>
			<div className="flex items-center gap-2.5 py-3.25 px-4 border-b border-(--edge)">
				<h2 className="font-heading text-base font-semibold m-0 flex-1">
					Alert inbox
				</h2>
				<span className="mono muted text-[11.5px]">{alerts.length} open</span>
				<button
					type="button"
					className="cmd-only-mobile text-muted-foreground text-[18px] leading-none px-0.5 hover:text-foreground"
					onClick={onClose}
					aria-label="Close inbox"
				>
					×
				</button>
			</div>
			<div className="flex-1 overflow-y-auto min-h-0">
				{alerts.length === 0 && (
					<div className="py-10 px-6 text-center text-muted-foreground text-[13px] leading-[1.6]">
						<div className="w-11.5 h-11.5 mx-auto mb-4 border border-border border-t-success rounded-full animate-[spin_3.2s_linear_infinite]" />
						No open alerts at this point in time. Scrub the timeline to replay
						the last 12 hours.
					</div>
				)}
				{alerts.map((a) => (
					<button
						type="button"
						key={a.alert_id}
						className="flex gap-2.5 w-full text-left py-2.75 pr-3.5 border-b border-[rgba(122,186,212,0.07)] items-start hover:bg-[rgba(143,246,255,0.04)]"
						onClick={() => onOpen(a)}
					>
						<span
							className="w-0.75 self-stretch shrink-0"
							style={SEV_BAR_STYLE[a.severity]}
						/>
						<div className="flex-1 min-w-0">
							<div className="text-[13.5px] leading-[1.3]">
								{alertTitle(a, assetTypeOf(a.asset_id))}
							</div>
							<div className="text-[11px] text-muted-foreground mt-0.75 mono">
								{sitesById.get(a.site_id)?.name ?? a.site_id} · {a.asset_id}
							</div>
						</div>
						<div className="flex flex-col items-end gap-1 pr-3.5 mono">
							<span className="text-[11.5px] text-muted-foreground">
								{formatDuration(at - openedAt(a))}
							</span>
							<span
								className="font-mono text-[10px] py-px px-1.5 border border-border text-muted-foreground"
								style={SEV_STATE_STYLE[a.severity]}
							>
								{a.severity}
							</span>
						</div>
					</button>
				))}
			</div>
			<div className="flex justify-between py-2.25 px-4 border-t border-(--edge) text-[11px] text-muted-foreground mono">
				<span>Polling every {LIVE_TICK_MS / 1000}s</span>
				<span>{critical} critical</span>
			</div>
		</>
	);
}
