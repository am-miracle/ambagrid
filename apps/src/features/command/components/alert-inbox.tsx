import type { Alert, AssetType } from "#/api/types";
import { formatDuration } from "#/lib/format";
import { LIVE_TICK_MS } from "#/lib/grid-tick";
import { alertTitle, type FleetSite, openedAt } from "../model";

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
			<div className="panel-head">
				<h2>Alert inbox</h2>
				<span className="mono muted">{alerts.length} open</span>
				<button
					type="button"
					className="x only-mobile"
					onClick={onClose}
					aria-label="Close inbox"
				>
					×
				</button>
			</div>
			<div className="alert-list">
				{alerts.length === 0 && (
					<div className="empty">
						<div className="empty-ring" />
						No open alerts at this point in time. Scrub the timeline to replay
						the last 12 hours.
					</div>
				)}
				{alerts.map((a) => (
					<button
						type="button"
						key={a.alert_id}
						className={`alert-row sev-${a.severity}`}
						onClick={() => onOpen(a)}
					>
						<span className="sev-bar" />
						<div className="alert-main">
							<div className="alert-title">
								{alertTitle(a, assetTypeOf(a.asset_id))}
							</div>
							<div className="alert-meta mono">
								{sitesById.get(a.site_id)?.name ?? a.site_id} · {a.asset_id}
							</div>
						</div>
						<div className="alert-right mono">
							<span className="age">{formatDuration(at - openedAt(a))}</span>
							<span className={`state sev-${a.severity}`}>{a.severity}</span>
						</div>
					</button>
				))}
			</div>
			<div className="inbox-foot mono">
				<span>Polling every {LIVE_TICK_MS / 1000}s</span>
				<span>{critical} critical</span>
			</div>
		</>
	);
}
