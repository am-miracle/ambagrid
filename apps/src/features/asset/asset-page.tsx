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

const PAGE_BG = {
	background:
		"radial-gradient(1200px 700px at 50% 0%, #0a1621 0%, var(--void) 68%)",
} as const;

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
			<div
				className="min-h-dvh max-w-230 mx-auto px-5 pt-4.5 pb-10 flex flex-col gap-3.5"
				style={PAGE_BG}
			>
				<BackLink />
				<p className="text-[#ff8b96] text-[13px]">
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
			<div
				className="min-h-dvh max-w-230 mx-auto px-5 pt-4.5 pb-10 flex flex-col gap-3.5"
				style={PAGE_BG}
			>
				<BackLink />
				<div className="bg-(--deck) border border-(--edge) px-4 py-3.5 h-60" />
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
		<div
			className="min-h-dvh max-w-230 mx-auto px-5 pt-4.5 pb-10 flex flex-col gap-3.5"
			style={PAGE_BG}
		>
			<BackLink />

			<header className="flex justify-between items-end gap-3">
				<div>
					<div className="mono muted text-[11.5px]">
						{siteId} · {ASSET_TYPE_LABEL[asset.assetType]}
					</div>
					<h1 className="mt-0.5 mb-0 text-[26px] font-semibold tracking-[0.3px]">
						{asset.assetId}
					</h1>
				</div>
				<div className="mono muted text-[11.5px]">
					last report {formatAge(asset.observedAt, now)}
				</div>
			</header>

			<section className="bg-(--deck) border border-(--edge) px-4 py-3.5 grid grid-cols-[repeat(auto-fill,minmax(170px,1fr))] gap-x-4.5 gap-y-2.5 mono">
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
					<p className="m-0 text-muted-foreground text-[12.5px]">
						Registered, but no {ASSET_TYPE_LABEL[asset.assetType].toLowerCase()}{" "}
						metrics reported yet.
					</p>
				)}
			</section>

			<section className="bg-(--deck) border border-(--edge) px-4 py-3.5">
				<div className="flex flex-wrap justify-between gap-2 mb-3">
					<div className="flex flex-wrap gap-1">
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
					<div className="flex flex-wrap gap-1">
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
				<div className="flex justify-between items-baseline gap-2.5">
					<h2 className="m-0 mb-2 text-base font-semibold">
						{METRIC_LABEL[active].label} ({METRIC_LABEL[active].unit})
					</h2>
					<span className="mono muted text-[11px]">
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

			<section className="bg-(--deck) border border-(--edge) px-4 py-3.5">
				<h2 className="m-0 mb-2 text-base font-semibold">Alert history</h2>
				{sortedHistory.length === 0 && (
					<p className="m-0 text-muted-foreground text-[12.5px]">
						No alerts on this asset.
					</p>
				)}
				<ul className="list-none m-0 p-0">
					{sortedHistory.map((a) => {
						const resolved = a.resolved_at ? Date.parse(a.resolved_at) : null;
						return (
							<li
								key={a.alert_id}
								className="flex gap-2.5 items-baseline py-2.25 border-t border-[rgba(122,186,212,0.07)]"
							>
								<StatusDot
									status={
										a.status === "open" ? severityStatus(a.severity) : "offline"
									}
									size={7}
								/>
								<div>
									<div className="text-[13.5px]">
										{alertTitle(a, asset.assetType)}
										<span className="mono muted text-[11px]">
											{" "}
											· {new Date(a.opened_at).toLocaleDateString()}{" "}
											{formatClock(Date.parse(a.opened_at))}
										</span>
									</div>
									<div className="text-[12.5px] text-[#b9cbd9] mt-0.5">
										{alertSummary(a)}
									</div>
									<div className="mono text-[11px] text-muted-foreground mt-0.75">
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
		<Link
			to="/"
			className="text-muted-foreground text-[12.5px] no-underline hover:text-primary"
		>
			← Fleet control
		</Link>
	);
}

function Field({ label, value }: { label: string; value: string }) {
	return (
		<div className="flex flex-col gap-0.5 text-[12.5px]">
			<span className="text-muted-foreground text-[11px]">{label}</span>
			<b className="font-medium">{value}</b>
		</div>
	);
}
