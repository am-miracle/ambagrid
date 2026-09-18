import {
	keepPreviousData,
	useMutation,
	useQueries,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { LIVE_TICK_MS, ROLLUP_TICK_MS } from "#/lib/grid-tick";
import { api, type ReadingsQuery } from "./client";
import { MAX_PAGE_SIZE } from "./contract";
import type { CollectionBody } from "./types";

// Follows next_cursor to exhaustion.
async function fetchAll<T>(
	fetchPage: (cursor?: string) => Promise<CollectionBody<T>>,
): Promise<T[]> {
	const items: T[] = [];
	let cursor: string | undefined;
	do {
		const page = await fetchPage(cursor);
		items.push(...page.data);
		cursor = page.page.next_cursor || undefined;
	} while (cursor);
	return items;
}

// /v1/sites aggregates across every asset and open alert on each call, so it
// polls on the slower rollup tick.
export const useSites = () =>
	useQuery({
		queryKey: ["sites"],
		queryFn: () =>
			fetchAll((cursor) => api.listSites({ limit: MAX_PAGE_SIZE, cursor })),
		refetchInterval: ROLLUP_TICK_MS,
	});

// Unscoped open alerts are cheap (served by a partial index), so this is the
// fast path for "what is wrong right now".
export const useFleetOpenAlerts = () =>
	useQuery({
		queryKey: ["alerts", "open"],
		queryFn: () =>
			fetchAll((cursor) =>
				api.listAlerts({ status: "open", limit: MAX_PAGE_SIZE, cursor }),
			),
		refetchInterval: LIVE_TICK_MS,
	});

// Unscoped history is capped at 25 rows, so replay history is read per site.
// One newest-first page per site covers the replay window in practice.
export const useSiteAlertHistories = (siteIds: string[]) =>
	useQueries({
		queries: siteIds.map((siteId) => ({
			queryKey: ["alerts", "history", "site", siteId],
			queryFn: () =>
				api
					.listAlerts({ site_id: siteId, limit: MAX_PAGE_SIZE })
					.then((body) => body.data),
			refetchInterval: ROLLUP_TICK_MS,
		})),
		combine: (results) => ({
			data: results.flatMap((r) => r.data ?? []),
			isLoading: results.some((r) => r.isLoading),
		}),
	});

// Resolves an asset's site and 404s once; live values then come from the
// site's telemetry subscription.
export const useAsset = (assetId: string) =>
	useQuery({
		queryKey: ["asset", assetId],
		queryFn: () => api.getAsset(assetId).then((body) => body.data),
	});

export const useReadings = (
	assetId: string | null,
	query: ReadingsQuery | null,
) =>
	useQuery({
		queryKey: ["readings", assetId, query],
		queryFn: () =>
			api
				.getReadings(assetId as string, query as ReadingsQuery)
				.then((body) => body.data),
		enabled: assetId !== null && query !== null,
		placeholderData: keepPreviousData,
	});

export const useAssetAlertHistory = (assetId: string | null) =>
	useQuery({
		queryKey: ["alerts", "history", "asset", assetId],
		queryFn: () =>
			fetchAll((cursor) =>
				api.listAlerts({
					asset_id: assetId as string,
					limit: MAX_PAGE_SIZE,
					cursor,
				}),
			),
		enabled: assetId !== null,
		refetchInterval: LIVE_TICK_MS,
	});

export const useAlert = (alertId: string | null) =>
	useQuery({
		queryKey: ["alert", alertId],
		queryFn: () => api.getAlert(alertId as string).then((body) => body.data),
		enabled: alertId !== null,
		refetchInterval: LIVE_TICK_MS,
	});

export const useResolveAlert = (alertId: string) => {
	const client = useQueryClient();
	return useMutation({
		mutationFn: ({ actorId, note }: { actorId: string; note: string }) =>
			api.resolveAlert(alertId, actorId, note),
		// A 409 means someone else already resolved it; refetching shows who.
		onSettled: () => {
			client.invalidateQueries({ queryKey: ["alert", alertId] });
			client.invalidateQueries({ queryKey: ["alerts"] });
			client.invalidateQueries({ queryKey: ["sites"] });
		},
	});
};
