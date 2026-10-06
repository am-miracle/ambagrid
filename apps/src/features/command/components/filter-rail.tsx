import type { ReactNode } from "react";
import { useState } from "react";
import { ASSET_TYPE_LABEL } from "#/api/contract";
import type { AssetType } from "#/api/types";
import { setActorId, useActorId } from "#/lib/actor";
import {
	ASSET_TYPES,
	type FleetSite,
	STATUS_ORDER,
	type Status,
} from "../model";
import { StatusDot } from "../ui";

export function FilterRail({
	open,
	siteSelected,
	statusCounts,
	visible,
	onToggleStatus,
	kindCounts,
	kinds,
	onToggleKind,
	unplaced,
	focusMode,
	canFocus,
	onToggleFocus,
	replaying,
	canReplay,
	onFleetView,
	onSiteView,
	onReplay,
}: {
	open: boolean;
	siteSelected: boolean;
	statusCounts: Record<Status, number>;
	visible: Record<Status, boolean>;
	onToggleStatus: (s: Status) => void;
	kindCounts: Record<AssetType, number>;
	kinds: Record<AssetType, boolean>;
	onToggleKind: (k: AssetType) => void;
	unplaced: FleetSite[];
	focusMode: boolean;
	canFocus: boolean;
	onToggleFocus: () => void;
	replaying: boolean;
	canReplay: boolean;
	onFleetView: () => void;
	onSiteView: () => void;
	onReplay: () => void;
}) {
	return (
		<aside
			className={`cmd-rail col-start-1 row-start-2 row-end-4 border-r border-(--edge) bg-(--deck) backdrop-blur-[10px] p-4 px-3 flex flex-col gap-4.5 overflow-y-auto z-20 panel-anim${open ? " open" : ""}`}
		>
			<RailGroup title="View">
				<button
					type="button"
					className={`border py-1.25 px-2.75 text-[12.5px] ${!siteSelected ? "text-primary-foreground bg-primary border-primary font-semibold" : "border-border text-muted-foreground"}`}
					onClick={onFleetView}
				>
					Fleet
				</button>
				<button
					type="button"
					className={`border py-1.25 px-2.75 text-[12.5px] ${siteSelected ? "text-primary-foreground bg-primary border-primary font-semibold" : "border-border text-muted-foreground"}`}
					disabled={!siteSelected}
					onClick={onSiteView}
				>
					Site
				</button>
			</RailGroup>

			<RailGroup title="Site status">
				{STATUS_ORDER.map((s) => (
					<button
						type="button"
						key={s}
						className={`flex items-center gap-2 w-full py-1.25 px-2 border text-[13px] capitalize ${visible[s] ? "text-foreground border-(--edge) bg-[rgba(143,246,255,0.04)]" : "border-transparent text-muted-foreground"} hover:border-border`}
						aria-pressed={visible[s]}
						onClick={() => onToggleStatus(s)}
					>
						<StatusDot
							status={s}
							className={visible[s] ? undefined : "opacity-30"}
						/>
						<span className="flex-1 text-left">{s}</span>
						<span className="mono text-[11.5px] text-muted-foreground">
							{statusCounts[s]}
						</span>
					</button>
				))}
			</RailGroup>

			<RailGroup title="Alerts by asset type">
				{ASSET_TYPES.map((k) => (
					<button
						type="button"
						key={k}
						className={`flex items-center gap-2 w-full py-1.25 px-2 border text-[13px] capitalize ${kinds[k] ? "text-foreground border-(--edge) bg-[rgba(143,246,255,0.04)]" : "border-transparent text-muted-foreground"} hover:border-border`}
						aria-pressed={kinds[k]}
						onClick={() => onToggleKind(k)}
					>
						<span className="flex-1 text-left">{ASSET_TYPE_LABEL[k]}</span>
						<span className="mono text-[11.5px] text-muted-foreground">
							{kindCounts[k]}
						</span>
					</button>
				))}
			</RailGroup>

			{unplaced.length > 0 && (
				<RailGroup title="Not on the map">
					{unplaced.map((s) => (
						<div
							key={s.id}
							className="flex justify-between gap-2 w-full py-1 px-2 text-[12.5px] text-muted-foreground"
						>
							<span>{s.name}</span>
							<span className="mono text-[10.5px] muted">
								{s.provisioningStatus}
							</span>
						</div>
					))}
				</RailGroup>
			)}

			<div className="mt-auto flex flex-col gap-2">
				<ActorField />
				<button
					type="button"
					className={`border py-1.25 px-2.75 text-[12.5px] w-full text-center ${focusMode ? "text-primary-foreground bg-primary border-primary font-semibold" : "border-border text-muted-foreground"}`}
					onClick={onToggleFocus}
					disabled={!canFocus}
				>
					Alert focus mode
				</button>
				<button
					type="button"
					className="w-full text-center bg-linear-to-b from-[#1d5f6d] to-[#113c47] border border-[#2f8ea3] text-[#d8fbff] py-2.25 font-semibold text-[13px] hover:from-[#24707f] hover:to-[#14495a]"
					onClick={onReplay}
					disabled={!canReplay || replaying}
				>
					{replaying ? "Replaying…" : "Run incident replay"}
				</button>
				<div className="text-2.75 text-muted-foreground leading-normal">
					Drag the field to orbit. Scroll to zoom.
				</div>
			</div>
		</aside>
	);
}

function RailGroup({
	title,
	children,
}: {
	title: string;
	children: ReactNode;
}) {
	return (
		<div>
			<div className="font-heading text-[11.5px] tracking-[0.6px] text-muted-foreground mb-2">
				{title}
			</div>
			<div className="flex flex-wrap gap-1">{children}</div>
		</div>
	);
}

function ActorField() {
	const actorId = useActorId();
	const [draft, setDraft] = useState(actorId);
	return (
		<form
			className="flex flex-col"
			onSubmit={(e) => {
				e.preventDefault();
				if (draft.trim()) setActorId(draft);
			}}
		>
			<label
				className="font-heading text-[11.5px] tracking-[0.6px] text-muted-foreground mb-2"
				htmlFor="actor-id"
			>
				Operator id
			</label>
			<div className="flex gap-1">
				<input
					id="actor-id"
					className="mono flex-1 min-w-0 bg-[rgba(4,8,13,0.8)] border border-border text-foreground py-1.25 px-2 text-xs focus:outline-none focus:border-primary"
					value={draft}
					onChange={(e) => setDraft(e.target.value)}
					placeholder="operator-0101"
					autoComplete="off"
				/>
				<button
					type="submit"
					className="border border-border py-1.25 px-2.75 text-[12.5px] text-muted-foreground"
					disabled={!draft.trim() || draft.trim() === actorId}
				>
					Set
				</button>
			</div>
		</form>
	);
}
