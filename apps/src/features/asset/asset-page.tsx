import { Link } from "@tanstack/react-router";
import { useMemo, useState } from "react";
import { ApiError } from "#/api/client";
import {
	ASSET_METRICS,
	ASSET_TYPE_LABEL,
	METRIC_LABEL,
	RANGE_PRESETS,
	SYSTEM_ACTOR,
} from "#/api/contract";
import { useAsset, useAssetAlertHistory, useReadings } from "#/api/queries";
import type { ReadingMetric } from "#/api/types";
import { ReadingChart } from "#/components/chart/reading-chart";
import {
	RELAY_LABEL,
	relayState,
} from "#/features/command/components/meter-cells";
import {
	alertSummary,
	alertTitle,
	formatClock,
	STATUS_COLOR,
	severityStatus,
} from "#/features/command/model";
import { StatusDot } from "#/features/command/ui";
import { formatDuration, formatMetric } from "#/lib/format";
import { formatAge, useNow } from "#/lib/grid-tick";
import { toAssetTelemetry } from "#/lib/telemetry";
import { useSiteTelemetry } from "#/lib/use-site-telemetry";

import "./asset.css";

export function AssetPage({ assetId }: { assetId: string }) {
	const now = useNow();
	// Resolves the asset's site and 404s once; live values then come from the
	// site's telemetry subscription, same as the fleet map.
	const { data: registered, error, isLoading } = useAsset(assetId);
	const { data: history } = useAssetAlertHistory(assetId);
	const [presetId, setPresetId] = useState(RANGE_PRESETS[1].id);
	const [metric, setMetric] = useState<ReadingMetric | null>(null);

	const siteId = registered?.site_id;
	const { snapshots } = useSiteTelemetry(siteId ? [siteId] : []);
	// Falls back to the one-time fetch until the first live snapshot arrives,
	// so the page paints immediately instead of waiting on the subscription.
	const asset =
		(siteId &&
			snapshots.get(siteId)?.assets.find((a) => a.assetId === assetId)) ||
		(registered ? toAssetTelemetry(registered) : null);

	const preset =
		RANGE_PRESETS.find((p) => p.id === presetId) ?? RANGE_PRESETS[1];
	const assetType = asset?.assetType ?? registered?.asset_type;
	const metrics = assetType ? ASSET_METRICS[assetType] : [];
	const active = metric ?? metrics[0]?.metric ?? "internal_temperature";

	// Anchored to page load and range changes, not the 1s clock, so the chart
	// does not refetch every second.
	const range = useMemo(() => {
		const to = Date.now();
		return {
			from: new Date(to - preset.windowMs).toISOString(),
			to: new Date(to).toISOString(),
		};
	}, [preset.windowMs]);

	const { data: series, isFetching } = useReadings(
		registered ? assetId : null,
		{
			metric: active,
			interval: preset.interval,
			...range,
		},
	);

	if (error) {
		return (
			<div className="asset-page">
				<BackLink />
				<p className="asset-error">
					{error instanceof ApiError && error.status === 404
						? `No asset ${assetId} has reported yet.`
						: error.message}
					{error instanceof ApiError &&
						error.requestId &&
						` (request ${error.requestId})`}
				</p>
			</div>
		);
	}
	if (isLoading || !asset) {
		return (
			<div className="asset-page">
				<BackLink />
				<div className="asset-deck asset-loading" />
			</div>
		);
	}

	const isMeter = asset.assetType === "smart_meter";
	const isBattery = asset.assetType === "battery_bms";
	const isInverter = asset.assetType === "solar_inverter";
	const relay = isMeter ? relayState(asset) : null;
	// undefined means the type-specific block has never reported at all,
	// distinct from a reported block with a null reading.
	const reportsTypeMetrics =
		(isMeter && asset.voltage !== undefined) ||
		(isBattery && asset.batterySocPct !== undefined) ||
		(isInverter && asset.solarIrradiance !== undefined);
	const sortedHistory = [...(history ?? [])].sort((a, b) =>
		b.opened_at.localeCompare(a.opened_at),
	);

	return (
		<div className="asset-page">
			<BackLink />

			<header className="asset-head">
				<div>
					<div className="mono muted">
						{siteId} · {ASSET_TYPE_LABEL[asset.assetType]}
					</div>
					<h1>{asset.assetId}</h1>
				</div>
				<div className="mono muted">
					last report {formatAge(asset.observedAt, now)}
				</div>
			</header>

			<section className="asset-deck asset-fields mono">
				<Field
					label="Internal temperature"
					value={
						asset.internalTemperature === null
							? "—"
							: `${asset.internalTemperature.toFixed(1)} °C`
					}
				/>
				{isMeter && relay && (
					<>
						<Field label="Relay" value={RELAY_LABEL[relay]} />
						<Field label="Household" value={asset.reportedHouseholdId ?? "—"} />
						<Field
							label="Voltage"
							value={formatMetric("voltage", asset.voltage)}
						/>
						<Field
							label="Current"
							value={formatMetric("current", asset.current)}
						/>
						<Field
							label="Active power"
							value={formatMetric("active_power", asset.activePower)}
						/>
						<Field
							label="Frequency"
							value={formatMetric("frequency", asset.frequency)}
						/>
						<Field
							label="Total energy"
							value={formatMetric("total_kwh", asset.totalKwh)}
						/>
					</>
				)}
				{isBattery && (
					<Field
						label="State of charge"
						value={formatMetric("battery_soc_pct", asset.batterySocPct)}
					/>
				)}
				{isInverter && (
					<Field
						label="Solar irradiance"
						value={formatMetric("solar_irradiance", asset.solarIrradiance)}
					/>
				)}
				{!reportsTypeMetrics && (
					<p className="asset-note">
						Registered, but no {ASSET_TYPE_LABEL[asset.assetType].toLowerCase()}{" "}
						metrics reported yet.
					</p>
				)}
			</section>

			<section className="asset-deck">
				<div className="asset-controls">
					<div className="asset-chips">
						{metrics.map((m) => (
							<button
								type="button"
								key={m.metric}
								className={`chip${active === m.metric ? " on" : ""}`}
								onClick={() => setMetric(m.metric)}
							>
								{METRIC_LABEL[m.metric].label}
							</button>
						))}
					</div>
					<div className="asset-chips">
						{RANGE_PRESETS.map((p) => (
							<button
								type="button"
								key={p.id}
								className={`chip${preset.id === p.id ? " on" : ""}`}
								onClick={() => setPresetId(p.id)}
							>
								{p.label}
							</button>
						))}
					</div>
				</div>
				<div className="asset-chart-head">
					<h2>
						{METRIC_LABEL[active].label} ({METRIC_LABEL[active].unit})
					</h2>
					<span className="mono muted">
						{series?.aggregation === "max" ? "maximum" : "average"} per{" "}
						{preset.interval}
					</span>
				</div>
				<div
					style={{ opacity: isFetching ? 0.6 : 1, transition: "opacity 0.2s" }}
				>
					<ReadingChart
						points={series?.points ?? []}
						unit={METRIC_LABEL[active].unit}
						label={METRIC_LABEL[active].label}
					/>
				</div>
			</section>

			<section className="asset-deck">
				<h2>Alert history</h2>
				{sortedHistory.length === 0 && (
					<p className="asset-note">No alerts on this asset.</p>
				)}
				<ul className="asset-history">
					{sortedHistory.map((a) => {
						const resolved = a.resolved_at ? Date.parse(a.resolved_at) : null;
						return (
							<li key={a.alert_id}>
								<StatusDot
									status={
										a.status === "open" ? severityStatus(a.severity) : "offline"
									}
									size={7}
								/>
								<div>
									<div className="asset-history-title">
										{alertTitle(a, asset.assetType)}
										<span className="mono muted">
											{" "}
											· {new Date(a.opened_at).toLocaleDateString()}{" "}
											{formatClock(Date.parse(a.opened_at))}
										</span>
									</div>
									<div className="asset-history-text">{alertSummary(a)}</div>
									<div className="mono asset-history-meta">
										{a.status === "open" ? (
											<span
												style={{
													color: STATUS_COLOR[severityStatus(a.severity)],
												}}
											>
												open for {formatDuration(now - Date.parse(a.opened_at))}
											</span>
										) : (
											<>
												{a.resolved_by === SYSTEM_ACTOR
													? "recovered"
													: `resolved by ${a.resolved_by}`}
												{resolved !== null &&
													` after ${formatDuration(resolved - Date.parse(a.opened_at))}`}
												{a.resolved_by !== SYSTEM_ACTOR &&
													a.resolution_note &&
													` · ${a.resolution_note}`}
											</>
										)}
									</div>
								</div>
							</li>
						);
					})}
				</ul>
			</section>
		</div>
	);
}

function BackLink() {
	return (
		<Link to="/" className="asset-back">
			← Fleet control
		</Link>
	);
}

function Field({ label, value }: { label: string; value: string }) {
	return (
		<div className="asset-field">
			<span>{label}</span>
			<b>{value}</b>
		</div>
	);
}
