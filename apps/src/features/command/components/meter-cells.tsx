import { Link } from "@tanstack/react-router";
import type { CSSProperties } from "react";
import { useEffect, useRef } from "react";
import { gsap, prefersReducedMotion } from "#/lib/motion";
import type { AssetTelemetry } from "#/lib/telemetry";

export type RelayState = "closed" | "open" | "unknown" | "no_data";

export function relayState(asset: AssetTelemetry): RelayState {
	if (asset.relayClosed === undefined) return "no_data";
	if (asset.relayClosed === null) return "unknown";
	return asset.relayClosed ? "closed" : "open";
}

export const RELAY_LABEL: Record<RelayState, string> = {
	closed: "Supplying power",
	open: "Disconnected",
	unknown: "Relay not reported",
	no_data: "No meter metrics yet",
};

const RELAY_STYLE: Record<RelayState, CSSProperties> = {
	closed: {
		background: "var(--cyan)",
		borderColor: "var(--cyan)",
		boxShadow: "0 0 8px rgba(143,246,255,0.45)",
	},
	open: { background: "#04080d" },
	unknown: {},
	no_data: { borderStyle: "dashed" },
};

export function MeterCells({
	meters,
	alertingIds,
}: {
	meters: AssetTelemetry[];
	alertingIds: ReadonlySet<string>;
}) {
	if (meters.length === 0) return null;
	return (
		<div className="mt-3">
			<div className="flex justify-between text-[11px] text-muted-foreground font-heading mb-[6px]">
				<span>Household relays</span>
				<span className="mono muted">
					{meters.filter((m) => relayState(m) === "closed").length}/
					{meters.length} on
				</span>
			</div>
			<ul className="list-none m-0 p-0 grid grid-cols-[repeat(auto-fill,14px)] gap-1">
				{meters.map((meter) => (
					<li key={meter.assetId}>
						<MeterCell
							meter={meter}
							alerting={alertingIds.has(meter.assetId)}
						/>
					</li>
				))}
			</ul>
		</div>
	);
}

function MeterCell({
	meter,
	alerting,
}: {
	meter: AssetTelemetry;
	alerting: boolean;
}) {
	const state = relayState(meter);
	const ref = useRef<HTMLAnchorElement>(null);
	const previous = useRef(state);

	useEffect(() => {
		const was = previous.current;
		previous.current = state;
		if (was === state || !ref.current || prefersReducedMotion()) return;
		gsap.fromTo(
			ref.current,
			{ scale: state === "open" ? 1.35 : 0.6 },
			{ scale: 1, duration: 0.5, ease: "elastic.out(1, 0.4)" },
		);
	}, [state]);

	const label = `${meter.assetId}: ${RELAY_LABEL[state]}${alerting ? ", open alert" : ""}`;
	return (
		<Link
			ref={ref}
			to="/assets/$assetId"
			params={{ assetId: meter.assetId }}
			className={`block w-[14px] h-[14px] border border-border hover:border-foreground${state === "unknown" ? " meter-cell-unknown" : ""}`}
			style={{
				...RELAY_STYLE[state],
				...(alerting
					? {
							outline: "1.5px solid var(--critical)",
							outlineOffset: "1.5px",
						}
					: undefined),
			}}
			aria-label={label}
			title={label}
		/>
	);
}
