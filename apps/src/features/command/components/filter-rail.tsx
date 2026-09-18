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
		<aside className={`rail panel-anim${open ? " open" : ""}`}>
			<RailGroup title="View">
				<button
					type="button"
					className={`chip${!siteSelected ? " on" : ""}`}
					onClick={onFleetView}
				>
					Fleet
				</button>
				<button
					type="button"
					className={`chip${siteSelected ? " on" : ""}`}
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
						className={`rail-filter${visible[s] ? " on" : ""}`}
						aria-pressed={visible[s]}
						onClick={() => onToggleStatus(s)}
					>
						<StatusDot status={s} />
						<span className="rail-filter-label">{s}</span>
						<span className="mono rail-filter-count">{statusCounts[s]}</span>
					</button>
				))}
			</RailGroup>

			<RailGroup title="Alerts by asset type">
				{ASSET_TYPES.map((k) => (
					<button
						type="button"
						key={k}
						className={`rail-filter${kinds[k] ? " on" : ""}`}
						aria-pressed={kinds[k]}
						onClick={() => onToggleKind(k)}
					>
						<span className="rail-filter-label">{ASSET_TYPE_LABEL[k]}</span>
						<span className="mono rail-filter-count">{kindCounts[k]}</span>
					</button>
				))}
			</RailGroup>

			{unplaced.length > 0 && (
				<RailGroup title="Not on the map">
					{unplaced.map((s) => (
						<div key={s.id} className="unplaced">
							<span>{s.name}</span>
							<span className="mono muted">{s.provisioningStatus}</span>
						</div>
					))}
				</RailGroup>
			)}

			<div className="rail-foot">
				<ActorField />
				<button
					type="button"
					className={`chip wide${focusMode ? " on" : ""}`}
					onClick={onToggleFocus}
					disabled={!canFocus}
				>
					Alert focus mode
				</button>
				<button
					type="button"
					className="primary wide"
					onClick={onReplay}
					disabled={!canReplay || replaying}
				>
					{replaying ? "Replaying…" : "Run incident replay"}
				</button>
				<div className="rail-hint">
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
		<div className="rail-group">
			<div className="rail-title">{title}</div>
			<div className="rail-body">{children}</div>
		</div>
	);
}

// Stand-in for the gateway-injected X-Actor-Id until operator auth exists.
function ActorField() {
	const actorId = useActorId();
	const [draft, setDraft] = useState(actorId);
	return (
		<form
			className="actor"
			onSubmit={(e) => {
				e.preventDefault();
				if (draft.trim()) setActorId(draft);
			}}
		>
			<label className="rail-title" htmlFor="actor-id">
				Operator id
			</label>
			<div className="actor-row">
				<input
					id="actor-id"
					className="mono"
					value={draft}
					onChange={(e) => setDraft(e.target.value)}
					placeholder="operator-0101"
					autoComplete="off"
				/>
				<button
					type="submit"
					className="chip"
					disabled={!draft.trim() || draft.trim() === actorId}
				>
					Set
				</button>
			</div>
		</form>
	);
}
