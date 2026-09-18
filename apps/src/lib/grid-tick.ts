import { useSyncExternalStore } from "react";

// Poll cadence for alert and site-rollup reads. Asset telemetry has its own
// transport seam in lib/telemetry.ts.
export const LIVE_TICK_MS = 5_000;
export const ROLLUP_TICK_MS = 30_000;

// Telemetry older than this reads as fully stale. The simulator reports every
// 5s, so a minute is twelve missed reports.
export const STALE_AFTER_MS = 60_000;

let now = Date.now();
const listeners = new Set<() => void>();
let timer: ReturnType<typeof setInterval> | undefined;

function subscribe(listener: () => void) {
	listeners.add(listener);
	if (!timer) {
		timer = setInterval(() => {
			now = Date.now();
			for (const notify of listeners) notify();
		}, 1_000);
	}
	return () => {
		listeners.delete(listener);
		if (listeners.size === 0 && timer) {
			clearInterval(timer);
			timer = undefined;
		}
	};
}

export const useNow = () =>
	useSyncExternalStore(
		subscribe,
		() => now,
		() => now,
	);

// 1 when just seen, decaying to 0 at STALE_AFTER_MS; 0 when never seen.
export function freshness(lastSeenAt: string | null, at: number): number {
	if (!lastSeenAt) return 0;
	const age = at - Date.parse(lastSeenAt);
	return Math.max(0, Math.min(1, 1 - (age - LIVE_TICK_MS) / STALE_AFTER_MS));
}

export function formatAge(lastSeenAt: string | null, at: number): string {
	if (!lastSeenAt) return "never reported";
	const seconds = Math.max(0, Math.round((at - Date.parse(lastSeenAt)) / 1000));
	if (seconds < 60) return `${seconds}s ago`;
	const minutes = Math.round(seconds / 60);
	if (minutes < 60) return `${minutes}m ago`;
	const hours = Math.round(minutes / 60);
	if (hours < 48) return `${hours}h ago`;
	return `${Math.round(hours / 24)}d ago`;
}
