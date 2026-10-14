import { formatAge } from "#/lib/grid-tick";
import type { FleetSite, Status } from "../model";
import { STATUS_COLOR } from "../model";
import { PanelHeader, PanelTitle, StatusDot } from "../ui";

interface FleetTableProps {
	sites: FleetSite[];
	statuses: Record<string, Status>;
	now: number;
	onSelectSite: (id: string) => void;
}

const HEALTH_TONE: Record<string, Status> = {
	live: "healthy",
	delayed: "warning",
	offline: "offline",
};

function toIso(ts: number | null): string | null {
	return ts ? new Date(ts).toISOString() : null;
}

function sortSites(
	sites: FleetSite[],
	statuses: Record<string, Status>,
): FleetSite[] {
	return [...sites].sort((a, b) => {
		const aOff = statuses[a.id] === "offline" ? 0 : 1;
		const bOff = statuses[b.id] === "offline" ? 0 : 1;
		if (aOff !== bOff) return aOff - bOff;
		if (b.queueDepth !== a.queueDepth) return b.queueDepth - a.queueDepth;
		return a.name.localeCompare(b.name);
	});
}

export function FleetTable({
	sites,
	statuses,
	now,
	onSelectSite,
}: FleetTableProps) {
	const sorted = sortSites(sites, statuses);
	return (
		<>
			<PanelHeader>
				<PanelTitle>Fleet health</PanelTitle>
				<span className="mono muted text-[11.5px]">{sites.length} sites</span>
			</PanelHeader>
			<div className="flex-1 overflow-y-auto min-h-0">
				<div className="grid grid-cols-[auto_1fr_auto_auto_auto_auto] gap-x-3 px-4 py-2 text-[11px] font-heading text-muted-foreground border-b border-(--edge)">
					<span />
					<span>Site</span>
					<span>Gateway</span>
					<span>Contact</span>
					<span>Queue</span>
					<span>Oldest</span>
				</div>
				{sorted.map((s) => {
					const tone = HEALTH_TONE[s.healthStatus] ?? "offline";
					return (
						<button
							type="button"
							key={s.id}
							className="grid grid-cols-[auto_1fr_auto_auto_auto_auto] gap-x-3 items-center w-full text-left px-4 py-2 border-b border-[rgba(122,186,212,0.07)] hover:bg-[rgba(143,246,255,0.04)] text-[11.5px]"
							onClick={() => onSelectSite(s.id)}
						>
							<StatusDot status={statuses[s.id] ?? "offline"} size={6} />
							<span className="truncate">{s.name}</span>
							<span className="mono" style={{ color: STATUS_COLOR[tone] }}>
								{s.healthStatus}
							</span>
							<span className="mono muted">
								{formatAge(toIso(s.lastContactAt), now)}
							</span>
							<span className="mono muted">
								{s.queueDepth}
								{s.queueGrowing && (
									<span className="text-warning ml-0.5">↑</span>
								)}
							</span>
							<span className="mono muted">
								{s.oldestPendingAt
									? formatAge(toIso(s.oldestPendingAt), now)
									: "—"}
							</span>
						</button>
					);
				})}
			</div>
		</>
	);
}
