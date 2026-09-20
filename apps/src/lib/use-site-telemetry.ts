import { useEffect, useRef, useState } from "react";
import type { SiteTelemetrySnapshot, TelemetrySource } from "./telemetry";
import { telemetrySource } from "./telemetry";

export interface SiteTelemetryState {
	snapshots: ReadonlyMap<string, SiteTelemetrySnapshot>;
	errors: ReadonlyMap<string, Error>;
}

const EMPTY: SiteTelemetryState = { snapshots: new Map(), errors: new Map() };

// Live asset state for a set of sites, through the TelemetrySource seam so the
// UI does not care whether snapshots arrive by polling or SSE.
export function useSiteTelemetry(
	siteIds: readonly string[],
	source: TelemetrySource = telemetrySource,
): SiteTelemetryState {
	const [state, setState] = useState<SiteTelemetryState>(EMPTY);
	const latest = useRef(state);
	latest.current = state;
	const key = siteIds.join("\n");

	useEffect(() => {
		const ids = key ? key.split("\n") : [];
		const snapshots = new Map<string, SiteTelemetrySnapshot>(
			ids.flatMap((id) => {
				const known = latest.current.snapshots.get(id);
				return known ? [[id, known] as const] : [];
			}),
		);
		const errors = new Map<string, Error>();
		let frame = 0;

		const flush = () => {
			if (frame) return;
			frame = requestAnimationFrame(() => {
				frame = 0;
				setState({ snapshots: new Map(snapshots), errors: new Map(errors) });
			});
		};

		const unsubscribes = ids.map((siteId) =>
			source.subscribe(siteId, {
				onSnapshot(snapshot) {
					snapshots.set(siteId, snapshot);
					errors.delete(siteId);
					flush();
				},
				onError(error) {
					errors.set(siteId, error);
					flush();
				},
			}),
		);

		return () => {
			for (const unsubscribe of unsubscribes) unsubscribe();
			cancelAnimationFrame(frame);
		};
	}, [key, source]);

	return state;
}
