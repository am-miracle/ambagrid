import { Link } from "@tanstack/react-router";
import { useEffect, useRef } from "react";
import { gsap, prefersReducedMotion } from "#/lib/motion";
import type { AssetTelemetry } from "#/lib/telemetry";

export type RelayState = "closed" | "open" | "unknown" | "no_data";

// relayClosed is undefined when the meter has not reported its metrics block,
// and null when the block arrived without a relay reading.
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

// One cell per household meter. Past a few hundred meters a site needs a
// canvas layer instead; the reference fleet tops out at tens per site.
export function MeterCells({
	meters,
	alertingIds,
}: {
	meters: AssetTelemetry[];
	alertingIds: ReadonlySet<string>;
}) {
	if (meters.length === 0) return null;
	return (
		<div className="meters">
			<div className="meters-head">
				<span>Household relays</span>
				<span className="mono muted">
					{meters.filter((m) => relayState(m) === "closed").length}/
					{meters.length} on
				</span>
			</div>
			<ul className="meter-cells">
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
		// A house losing power should read as a snap, not a fade.
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
			className={`meter-cell relay-${state}${alerting ? " alerting" : ""}`}
			aria-label={label}
			title={label}
		/>
	);
}
