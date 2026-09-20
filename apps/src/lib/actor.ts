import { useSyncExternalStore } from "react";

// Stand-in for the gateway that will authenticate operators and inject
// X-Actor-Id. The API trusts that header, so this must not ship to an
// internet-facing deployment as the source of identity.
const KEY = "ambagrid.actor-id";
const listeners = new Set<() => void>();

export function setActorId(value: string) {
	localStorage.setItem(KEY, value.trim());
	for (const notify of listeners) notify();
}

export const useActorId = () =>
	useSyncExternalStore(
		(listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		() => localStorage.getItem(KEY) ?? "",
		() => "",
	);
