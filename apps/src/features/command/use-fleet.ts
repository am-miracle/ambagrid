import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo } from "react";
import {
	useFleetOpenAlerts,
	useSiteAlertHistories,
	useSites,
} from "#/api/queries";
import type { Alert } from "#/api/types";
import type { AssetTelemetry } from "#/lib/telemetry";
import { useSiteTelemetry } from "#/lib/use-site-telemetry";
import { buildSites } from "./model";

export function useFleet() {
	const client = useQueryClient();
	const sites = useSites();
	const open = useFleetOpenAlerts();
	const siteIds = useMemo(
		() => (sites.data ?? []).map((s) => s.site_id),
		[sites.data],
	);
	const telemetry = useSiteTelemetry(siteIds);
	const history = useSiteAlertHistories(siteIds);

	// History polls slowly; when the fast open-alerts read changes membership,
	// refresh history so resolutions show up with their real resolved_at.
	const openSignature = (open.data ?? [])
		.map((a) => a.alert_id)
		.sort()
		.join(",");
	useEffect(() => {
		if (!openSignature) return;
		client.invalidateQueries({ queryKey: ["alerts", "history", "site"] });
	}, [openSignature, client]);

	const fleetSites = useMemo(
		() => buildSites(sites.data ?? [], telemetry.snapshots),
		[sites.data, telemetry.snapshots],
	);

	const alerts = useMemo(() => {
		const byId = new Map<string, Alert>();
		for (const alert of history.data) byId.set(alert.alert_id, alert);
		for (const alert of open.data ?? []) byId.set(alert.alert_id, alert);
		return [...byId.values()];
	}, [history.data, open.data]);

	const assetsById = useMemo(() => {
		const map = new Map<string, AssetTelemetry & { siteId: string }>();
		for (const snapshot of telemetry.snapshots.values()) {
			for (const asset of snapshot.assets) {
				map.set(asset.assetId, { ...asset, siteId: snapshot.siteId });
			}
		}
		return map;
	}, [telemetry.snapshots]);

	const telemetryError =
		telemetry.errors.size > 0 && telemetry.snapshots.size === 0
			? [...telemetry.errors.values()][0]
			: null;

	return {
		sites: fleetSites,
		alerts,
		assetsById,
		ready:
			sites.isSuccess &&
			open.isSuccess &&
			siteIds.every(
				(id) => telemetry.snapshots.has(id) || telemetry.errors.has(id),
			),
		error: sites.error ?? open.error ?? telemetryError,
	};
}
