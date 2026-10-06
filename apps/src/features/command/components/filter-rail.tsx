import type { ReactNode } from "react";
import { useState } from "react";
import { ASSET_TYPE_LABEL } from "#/api/contract";
import type { AssetType } from "#/api/types";
import { Button } from "#/components/ui/button";
import { Input } from "#/components/ui/input";
import { Label } from "#/components/ui/label";
import { Toggle } from "#/components/ui/toggle";
import { setActorId, useActorId } from "#/lib/actor";
import {
	ASSET_TYPES,
	type FleetSite,
	STATUS_ORDER,
	type Status,
} from "../model";
import { SectionTitle, StatusDot } from "../ui";

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
				<Toggle
					variant="chip"
					pressed={!siteSelected}
					onPressedChange={onFleetView}
				>
					Fleet
				</Toggle>
				<Toggle
					variant="chip"
					pressed={siteSelected}
					disabled={!siteSelected}
					onPressedChange={onSiteView}
				>
					Site
				</Toggle>
			</RailGroup>

			<RailGroup title="Site status">
				{STATUS_ORDER.map((s) => (
					<Toggle
						key={s}
						className="capitalize"
						pressed={visible[s]}
						onPressedChange={() => onToggleStatus(s)}
					>
						<StatusDot
							status={s}
							className={visible[s] ? undefined : "opacity-30"}
						/>
						<span className="flex-1 text-left">{s}</span>
						<span className="mono text-[11.5px] text-muted-foreground">
							{statusCounts[s]}
						</span>
					</Toggle>
				))}
			</RailGroup>

			<RailGroup title="Alerts by asset type">
				{ASSET_TYPES.map((k) => (
					<Toggle
						key={k}
						pressed={kinds[k]}
						onPressedChange={() => onToggleKind(k)}
					>
						<span className="flex-1 text-left">{ASSET_TYPE_LABEL[k]}</span>
						<span className="mono text-[11.5px] text-muted-foreground">
							{kindCounts[k]}
						</span>
					</Toggle>
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
				<Toggle
					variant="chip"
					className="w-full"
					pressed={focusMode}
					onPressedChange={onToggleFocus}
					disabled={!canFocus}
				>
					Alert focus mode
				</Toggle>
				<Button
					variant="command"
					size="lg"
					className="w-full"
					onClick={onReplay}
					disabled={!canReplay || replaying}
				>
					{replaying ? "Replaying…" : "Run incident replay"}
				</Button>
				<div className="text-[11px] text-muted-foreground leading-normal">
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
			<SectionTitle>{title}</SectionTitle>
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
			<Label className="mb-2" htmlFor="actor-id">
				Operator id
			</Label>
			<div className="flex gap-1">
				<Input
					id="actor-id"
					className="mono flex-1"
					value={draft}
					onChange={(e) => setDraft(e.target.value)}
					placeholder="operator-0101"
					autoComplete="off"
				/>
				<Button
					type="submit"
					variant="muted"
					disabled={!draft.trim() || draft.trim() === actorId}
				>
					Set
				</Button>
			</div>
		</form>
	);
}
