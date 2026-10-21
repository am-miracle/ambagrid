import { formatAge } from "#/lib/grid-tick";
import type { FleetSite, Status } from "../model";
import { healthStatusTone, STATUS_COLOR, toIso } from "../model";
import { PanelHeader, PanelTitle, StatusDot } from "../ui";

interface FleetTableProps {
	sites: FleetSite[];
	statuses: Record<string, Status>;
	now: number;
	onSelectSite: (id: string) => void;
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

const COL_GRID =
	"grid grid-cols-[auto_1fr_auto_auto_auto_auto] gap-x-3" as const;

export function FleetTable({
	sites,
	statuses,
	now,
	onSelectSite,
}: FleetTableProps) {
	const sorted = sortSites(sites, statuses);
	const firstOnlineIdx = sorted.findIndex((s) => statuses[s.id] !== "offline");
	const hasOffline =
		firstOnlineIdx > 0 || (firstOnlineIdx === -1 && sorted.length > 0);

	return (
		<>
			<PanelHeader>
				<PanelTitle>Fleet health</PanelTitle>
				<span className="mono muted text-[11.5px]">{sites.length} sites</span>
			</PanelHeader>
			<div className="flex-1 overflow-y-auto min-h-0">
				<div
					className={`${COL_GRID} px-4 py-2 text-[11px] font-heading text-muted-foreground border-b border-(--edge) sticky top-0 bg-(--deck) backdrop-blur-sm z-10`}
				>
					<span />
					<span>Site</span>
					<span>Health</span>
					<span>Contact</span>
					<span>Queue</span>
					<span>Oldest</span>
				</div>
				{sorted.map((s, i) => {
					const tone = healthStatusTone(s.healthStatus);
					const isOffline = statuses[s.id] === "offline";
					const showDivider =
						!isOffline &&
						hasOffline &&
						i === (firstOnlineIdx === -1 ? sorted.length : firstOnlineIdx);

					return (
						<div key={s.id}>
							{showDivider && (
								<div className="px-4 py-1.5 text-[10px] font-heading tracking-[0.4px] text-muted-foreground/60 uppercase border-b border-(--edge) bg-white/2">
									Online
								</div>
							)}
							<button
								type="button"
								className={`${COL_GRID} items-center w-full text-left px-4 py-2 border-b border-[rgba(122,186,212,0.07)] hover:bg-[rgba(143,246,255,0.04)] text-[11.5px]${isOffline ? " opacity-60" : ""}`}
								onClick={() => onSelectSite(s.id)}
							>
								<StatusDot status={statuses[s.id] ?? "offline"} size={6} />
								<span className="truncate">{s.name}</span>
								<span className="mono" style={{ color: STATUS_COLOR[tone] }}>
									{s.healthStatus}
								</span>
								<span className="mono muted">
									{isOffline && !s.lastContactAt
										? "—"
										: formatAge(toIso(s.lastContactAt), now)}
								</span>
								<span
									className="mono"
									style={
										s.queueDepth > 0
											? { color: STATUS_COLOR.warning }
											: undefined
									}
								>
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
						</div>
					);
				})}
			</div>
		</>
	);
}
