import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useAssetAlertHistory } from "#/api/queries";
import type { Alert, AssetType } from "#/api/types";
import { humanizeKind } from "#/lib/format";
import { useNow } from "#/lib/grid-tick";
import { gsap } from "#/lib/motion";
import { AlertDetail } from "./components/alert-detail";
import { AlertInbox } from "./components/alert-inbox";
import { FilterRail } from "./components/filter-rail";
import { PaymentPanel } from "./components/payment-panel";
import { RevenuePanel } from "./components/revenue-panel";
import { SiteCard } from "./components/site-card";
import { Timeline } from "./components/timeline";
import { type FleetKpis, Topbar } from "./components/topbar";
import {
	ASSET_TYPES,
	alertTitle,
	assetFreshAt,
	buildEvents,
	formatClock,
	isOpenAt,
	openedAt,
	proximityLinks,
	REPLAY_WINDOW_MS,
	STATUS_COLOR,
	type Status,
	severityStatus,
	siteStatusAt,
	statusBands,
} from "./model";
import {
	type CameraApi,
	CommandScene,
	type SceneSite,
} from "./scene/command-scene";
import { Boot, Sparkline, StatusDot } from "./ui";
import { useAlertSeries } from "./use-alert-series";
import { useFleet } from "./use-fleet";

const WEIGHT: Record<Status, number> = {
	healthy: 1,
	warning: 0.78,
	critical: 0.45,
	offline: 0,
};
const ALL_STATUSES: Record<Status, boolean> = {
	healthy: true,
	warning: true,
	critical: true,
	offline: true,
};
const ALL_KINDS = Object.fromEntries(
	ASSET_TYPES.map((k) => [k, true]),
) as Record<AssetType, boolean>;
const LIVE_SNAP_MS = 30_000;
const PLAYBACK_FRAMES = 290;

export function CommandSurface() {
	const fleet = useFleet();
	const now = useNow();

	const [booted, setBooted] = useState(false);
	const [cursor, setCursor] = useState<number | null>(null);
	const [playing, setPlaying] = useState(false);
	const [selectedSite, setSelectedSite] = useState<string | null>(null);
	const [selectedAlert, setSelectedAlert] = useState<string | null>(null);
	const [focusMode, setFocusMode] = useState(false);
	const [visible, setVisible] = useState(ALL_STATUSES);
	const [kinds, setKinds] = useState(ALL_KINDS);
	const [hover, setHover] = useState<{
		id: string;
		x: number;
		y: number;
	} | null>(null);
	const [toast, setToast] = useState<{ text: string; status: Status } | null>(
		null,
	);
	const [railOpen, setRailOpen] = useState(false);
	const [inboxOpen, setInboxOpen] = useState(false);
	const [paymentOpen, setPaymentOpen] = useState(false);
	const [scenario, setScenario] = useState(false);
	const [inboxTab, setInboxTab] = useState<"alerts" | "revenue">("alerts");

	const camRef = useRef<CameraApi | null>(null);
	const timersRef = useRef<number[]>([]);
	const toastTimer = useRef<number | undefined>(undefined);
	const scrubTween = useRef<gsap.core.Tween | null>(null);

	const windowStart = now - REPLAY_WINDOW_MS;
	const live = cursor === null;
	const at = cursor ?? now;

	const sitesById = useMemo(
		() => new Map(fleet.sites.map((s) => [s.id, s])),
		[fleet.sites],
	);
	const assetTypeOf = useCallback(
		(assetId: string) => fleet.assetsById.get(assetId)?.assetType,
		[fleet.assetsById],
	);

	const statuses = useMemo(() => {
		const m: Record<string, Status> = {};
		for (const s of fleet.sites) m[s.id] = siteStatusAt(s, fleet.alerts, at);
		return m;
	}, [fleet.sites, fleet.alerts, at]);

	const openAlerts = useMemo(
		() =>
			fleet.alerts
				.filter((a) => {
					const type = assetTypeOf(a.asset_id);
					return isOpenAt(a, at) && (!type || kinds[type]);
				})
				.sort((a, b) =>
					a.severity === b.severity
						? openedAt(b) - openedAt(a)
						: a.severity === "critical"
							? -1
							: b.severity === "critical"
								? 1
								: 0,
				),
		[fleet.alerts, at, kinds, assetTypeOf],
	);

	const kpi: FleetKpis = useMemo(() => {
		let weight = 0;
		let score = 0;
		let reporting = 0;
		let offlineAssets = 0;
		let disconnected = 0;
		for (const s of fleet.sites) {
			const st = statuses[s.id];
			const w = Math.max(1, s.meters.length);
			weight += w;
			score += w * WEIGHT[st];
			if (st !== "offline") reporting += 1;
			offlineAssets += s.assets.filter((a) => !assetFreshAt(a, at)).length;
			disconnected += s.disconnected;
		}
		return {
			health: weight ? (score / weight) * 100 : 0,
			critical: fleet.alerts.filter(
				(a) => a.severity === "critical" && isOpenAt(a, at),
			).length,
			offlineAssets,
			reporting,
			siteCount: fleet.sites.length,
			disconnected,
		};
	}, [fleet.sites, fleet.alerts, statuses, at]);

	const statusCounts = useMemo(() => {
		const c: Record<Status, number> = {
			healthy: 0,
			warning: 0,
			critical: 0,
			offline: 0,
		};
		for (const s of fleet.sites) c[statuses[s.id]] += 1;
		return c;
	}, [fleet.sites, statuses]);

	const kindCounts = useMemo(() => {
		const c = Object.fromEntries(ASSET_TYPES.map((k) => [k, 0])) as Record<
			AssetType,
			number
		>;
		for (const a of fleet.alerts) {
			const type = assetTypeOf(a.asset_id);
			if (type && isOpenAt(a, at)) c[type] += 1;
		}
		return c;
	}, [fleet.alerts, at, assetTypeOf]);

	const sceneSites: SceneSite[] = useMemo(
		() =>
			fleet.sites.flatMap((s) =>
				s.lat === null || s.lon === null
					? []
					: [
							{
								id: s.id,
								name: s.name,
								region: s.region,
								lat: s.lat,
								lon: s.lon,
								status: statuses[s.id],
								loadKw: s.loadKw,
								loadRatio: s.loadRatio,
								soc: s.soc,
								meterCount: s.meters.length,
							},
						],
			),
		[fleet.sites, statuses],
	);
	const unplaced = useMemo(
		() => fleet.sites.filter((s) => s.lat === null || s.lon === null),
		[fleet.sites],
	);
	const siteKey = fleet.sites.map((s) => s.id).join(",");
	// biome-ignore lint/correctness/useExhaustiveDependencies: the mesh depends on which sites exist, not on their live values
	const links = useMemo(() => proximityLinks(fleet.sites), [siteKey]);

	const events = useMemo(
		() => buildEvents(fleet.alerts, sitesById, windowStart, now),
		[fleet.alerts, sitesById, windowStart, now],
	);

	const alertObj = selectedAlert
		? (fleet.alerts.find((a) => a.alert_id === selectedAlert) ?? null)
		: null;
	const focusSiteId = alertObj ? alertObj.site_id : selectedSite;
	const focusSite = focusSiteId ? sitesById.get(focusSiteId) : undefined;
	const series = useAlertSeries(
		alertObj,
		alertObj ? assetTypeOf(alertObj.asset_id) : undefined,
		now,
	);
	const { data: assetHistory } = useAssetAlertHistory(
		alertObj?.asset_id ?? null,
	);
	const related = (assetHistory ?? []).filter(
		(a) => a.alert_id !== alertObj?.alert_id,
	);

	const bands = useMemo(() => {
		const site = selectedSite ? sitesById.get(selectedSite) : undefined;
		return site ? statusBands(site, fleet.alerts, windowStart, now) : null;
	}, [selectedSite, sitesById, fleet.alerts, windowStart, now]);

	const pushToast = useCallback((text: string, status: Status = "critical") => {
		setToast({ text, status });
		window.clearTimeout(toastTimer.current);
		toastTimer.current = window.setTimeout(() => setToast(null), 3600);
	}, []);

	const seenAlerts = useRef<Set<string> | null>(null);
	useEffect(() => {
		if (!fleet.ready) return;
		const open = fleet.alerts.filter((a) => a.status === "open");
		if (seenAlerts.current === null) {
			seenAlerts.current = new Set(open.map((a) => a.alert_id));
			return;
		}
		for (const a of open) {
			if (seenAlerts.current.has(a.alert_id)) continue;
			seenAlerts.current.add(a.alert_id);
			if (a.severity !== "critical") continue;
			const site = sitesById.get(a.site_id)?.name ?? a.site_id;
			pushToast(
				`Critical · ${site} · ${alertTitle(a, assetTypeOf(a.asset_id))} on ${a.asset_id}`,
			);
		}
	}, [fleet.ready, fleet.alerts, sitesById, assetTypeOf, pushToast]);

	const selectSite = useCallback((id: string | null) => {
		setSelectedSite(id);
		setSelectedAlert(null);
		setFocusMode(false);
		if (id) camRef.current?.focusSite(id);
		else camRef.current?.reset();
	}, []);

	const openAlert = useCallback((a: Alert) => {
		setSelectedAlert(a.alert_id);
		setSelectedSite(a.site_id);
		setFocusMode(true);
		setInboxOpen(true);
		camRef.current?.focusSite(a.site_id, true);
	}, []);

	const scrub = useCallback(
		(t: number) => {
			setPlaying(false);
			setCursor(t >= now - LIVE_SNAP_MS ? null : t);
		},
		[now],
	);

	const cursorRef = useRef(cursor);
	cursorRef.current = cursor;
	useEffect(() => {
		if (!playing) return;
		const step = REPLAY_WINDOW_MS / PLAYBACK_FRAMES;
		const id = window.setInterval(() => {
			const next = (cursorRef.current ?? Date.now()) + step;
			if (next >= Date.now() - LIVE_SNAP_MS) {
				setCursor(null);
				setPlaying(false);
			} else {
				setCursor(next);
			}
		}, 55);
		return () => window.clearInterval(id);
	}, [playing]);

	const togglePlay = () => {
		if (playing) {
			setPlaying(false);
			return;
		}
		if (live) setCursor(windowStart);
		setPlaying(true);
	};

	const replayTarget = useMemo(
		() =>
			[...fleet.alerts]
				.filter((a) => a.severity === "critical" && openedAt(a) > windowStart)
				.sort((a, b) => openedAt(b) - openedAt(a))[0] ?? null,
		[fleet.alerts, windowStart],
	);

	const runScenario = useCallback(() => {
		const target = replayTarget;
		if (!target) return;
		for (const t of timersRef.current) window.clearTimeout(t);
		timersRef.current = [];
		const opened = openedAt(target);
		const start = Math.max(
			Date.now() - REPLAY_WINDOW_MS,
			opened - 90 * 60 * 1000,
		);
		const site = sitesById.get(target.site_id)?.name ?? target.site_id;
		const title = alertTitle(target, assetTypeOf(target.asset_id));

		setScenario(true);
		setPlaying(false);
		setSelectedAlert(null);
		setSelectedSite(null);
		setFocusMode(false);
		setCursor(start);
		camRef.current?.reset();
		const later = (ms: number, fn: () => void) =>
			timersRef.current.push(window.setTimeout(fn, ms));

		later(500, () =>
			pushToast(`Replaying from ${formatClock(start)}`, "healthy"),
		);
		later(2000, () => {
			const proxy = { t: start };
			scrubTween.current?.kill();
			scrubTween.current = gsap.to(proxy, {
				t: opened + 60_000,
				duration: 2.4,
				ease: "power2.inOut",
				onUpdate: () => setCursor(proxy.t),
			});
		});
		later(4600, () =>
			pushToast(`Critical · ${site} · ${title} on ${target.asset_id}`),
		);
		later(5400, () => openAlert(target));
		later(10500, () => {
			pushToast("Back to live", "healthy");
			setCursor(null);
		});
		later(11000, () => setScenario(false));
	}, [replayTarget, sitesById, assetTypeOf, openAlert, pushToast]);

	useEffect(
		() => () => {
			for (const t of timersRef.current) window.clearTimeout(t);
			window.clearTimeout(toastTimer.current);
			scrubTween.current?.kill();
		},
		[],
	);

	const rootRef = useRef<HTMLDivElement | null>(null);
	useEffect(() => {
		if (!booted) return;
		gsap.fromTo(
			rootRef.current?.querySelectorAll(".panel-anim") ?? [],
			{ y: 18, opacity: 0 },
			{ y: 0, opacity: 1, duration: 0.7, stagger: 0.08, ease: "power3.out" },
		);
	}, [booted]);

	const meterTotal = fleet.sites.reduce((n, s) => n + s.meters.length, 0);
	const bootLines = [
		"Opening operator API",
		`Linking ${fleet.sites.length} sites · ${meterTotal.toLocaleString()} smart meters`,
		"Loading the last 12 hours of alerts",
		"Control surface ready",
	];
	const hoverSite = hover ? sitesById.get(hover.id) : undefined;
	const selected = selectedSite ? sitesById.get(selectedSite) : undefined;

	return (
		<div
			className="cmd-surface grid grid-cols-[210px_1fr_374px] grid-rows-[62px_1fr_154px] h-dvh bg-[radial-gradient(1200px_700px_at_50%_40%,#0a1621_0%,#05090e_68%)]"
			ref={rootRef}
		>
			{!booted && <Boot lines={bootLines} onDone={() => setBooted(true)} />}

			<Topbar
				kpi={kpi}
				at={at}
				live={live}
				openCount={openAlerts.length}
				onOpenPayment={() => setPaymentOpen(true)}
				onToggleRail={() => setRailOpen((v) => !v)}
				onToggleInbox={() => setInboxOpen((v) => !v)}
			/>

			<PaymentPanel open={paymentOpen} onClose={() => setPaymentOpen(false)} />

			<FilterRail
				open={railOpen}
				siteSelected={selectedSite !== null}
				statusCounts={statusCounts}
				visible={visible}
				onToggleStatus={(s) => setVisible((v) => ({ ...v, [s]: !v[s] }))}
				kindCounts={kindCounts}
				kinds={kinds}
				onToggleKind={(k) => setKinds((v) => ({ ...v, [k]: !v[k] }))}
				unplaced={unplaced}
				focusMode={focusMode}
				canFocus={focusSiteId !== null}
				onToggleFocus={() => setFocusMode((v) => !v)}
				replaying={scenario}
				canReplay={replayTarget !== null}
				onFleetView={() => selectSite(null)}
				onSiteView={() =>
					selectedSite && camRef.current?.focusSite(selectedSite, true)
				}
				onReplay={runScenario}
			/>

			<main className="cmd-stage col-start-2 row-start-2 relative overflow-hidden">
				<CommandScene
					sites={sceneSites}
					links={links}
					selectedId={selectedSite}
					focusAssetId={alertObj?.asset_id ?? null}
					focusSiteId={focusSiteId}
					focusMode={focusMode}
					visible={visible}
					onSelect={selectSite}
					onHover={(id, x, y) => setHover(id ? { id, x, y } : null)}
					registerCamera={(api) => {
						camRef.current = api;
					}}
				/>

				{fleet.error && (
					<div className="absolute top-14 left-1/2 -translate-x-1/2 max-w-[min(620px,90%)] py-2.5 px-3.5 bg-[rgba(40,8,14,0.92)] border border-[rgba(255,77,94,0.5)] text-[12.5px] leading-normal z-35">
						<b className="text-[#ff8b96] font-semibold">
							Operator API unreachable.
						</b>{" "}
						{fleet.error.message}. Start the API with{" "}
						<span className="mono">make api-serve</span>, or run the app with{" "}
						<span className="mono">VITE_USE_MOCKS=true</span>.
					</div>
				)}

				{focusMode && focusSite && (
					<div className="absolute top-3.5 left-4 text-[11.5px] text-primary border border-border bg-[rgba(5,12,18,0.8)] py-1.25 px-2.5 flex gap-2.5 items-center tracking-[0.4px] mono">
						focus · {focusSite.name}
						{alertObj ? ` · ${alertObj.asset_id}` : ""}
						<button
							type="button"
							className="text-muted-foreground text-[11px] hover:text-foreground"
							onClick={() => setFocusMode(false)}
						>
							exit
						</button>
					</div>
				)}

				{hover && hoverSite && !scenario && (
					<div
						className="fixed z-60 pointer-events-none bg-[rgba(6,12,18,0.94)] border border-border py-2.25 px-2.75 min-w-42.5 shadow-[0_18px_40px_rgba(0,0,0,0.55)]"
						style={{ left: hover.x + 14, top: hover.y - 10 }}
					>
						<div className="flex items-center gap-1.75 font-semibold text-[13.5px] mb-1.5">
							<StatusDot status={statuses[hoverSite.id]} />
							{hoverSite.name}
						</div>
						<div className="grid grid-cols-[1fr_auto] gap-x-3.5 gap-y-0.5 text-[11.5px] mono">
							<span className="text-muted-foreground">Load</span>
							<b>{hoverSite.loadKw.toFixed(1)} kW</b>
							<span className="text-muted-foreground">SoC</span>
							<b>
								{hoverSite.soc === null
									? "—"
									: `${Math.round(hoverSite.soc * 100)}%`}
							</b>
							<span className="text-muted-foreground">Irradiance</span>
							<b>
								{hoverSite.irradiance === null
									? "—"
									: `${Math.round(hoverSite.irradiance)} W/m²`}
							</b>
							<span className="text-muted-foreground">Meters</span>
							<b>{hoverSite.meters.length}</b>
							<span className="text-muted-foreground">Disconnected</span>
							<b>{hoverSite.disconnected}</b>
						</div>
					</div>
				)}

				{selected && !alertObj && (
					<SiteCard
						key={selected.id}
						site={selected}
						status={statuses[selected.id]}
						openAlerts={openAlerts.filter((a) => a.site_id === selected.id)}
						now={now}
						onOpenAlert={openAlert}
						onClose={() => selectSite(null)}
					/>
				)}

				{toast && (
					<output
						className="absolute top-3.5 left-1/2 -translate-x-1/2 flex items-center gap-2.25 py-2.25 px-3.75 bg-[rgba(6,12,18,0.95)] border text-[13px] shadow-[0_16px_40px_rgba(0,0,0,0.6)] animate-[toast-in_0.45s_cubic-bezier(0.2,0.9,0.3,1.4)] max-w-[min(560px,88%)] z-40"
						style={{ borderColor: STATUS_COLOR[toast.status] }}
					>
						<StatusDot status={toast.status} />
						{toast.text}
					</output>
				)}
			</main>

			<aside
				className={`cmd-inbox col-start-3 row-start-2 row-end-4 border-l border-(--edge) bg-(--deck) backdrop-blur-[10px] flex flex-col min-h-0 z-20 panel-anim${inboxOpen ? " open" : ""}`}
			>
				{alertObj ? (
					<AlertDetail
						key={alertObj.alert_id}
						alert={alertObj}
						site={sitesById.get(alertObj.site_id)}
						asset={fleet.assetsById.get(alertObj.asset_id)}
						at={at}
						series={series}
						related={related}
						onBack={() => {
							setSelectedAlert(null);
							setFocusMode(false);
						}}
						onOpenRelated={openAlert}
					/>
				) : (
					<>
						<div className="flex gap-1 px-4 pt-2.5 pb-0">
							{(["alerts", "revenue"] as const).map((t) => (
								<button
									key={t}
									type="button"
									className={`mono text-[11px] py-0.75 px-2.5 border ${
										inboxTab === t
											? "text-primary-foreground bg-primary border-primary"
											: "text-muted-foreground border-border hover:text-foreground hover:border-foreground"
									}`}
									onClick={() => setInboxTab(t)}
								>
									{t === "alerts" ? "Alerts" : "Revenue"}
								</button>
							))}
						</div>
						{inboxTab === "alerts" ? (
							<AlertInbox
								alerts={openAlerts}
								at={at}
								sitesById={sitesById}
								assetTypeOf={assetTypeOf}
								onOpen={openAlert}
								onClose={() => setInboxOpen(false)}
							/>
						) : (
							<RevenuePanel />
						)}
					</>
				)}
			</aside>

			<footer className="cmd-timeline col-start-2 row-start-3 border-t border-(--edge) bg-(--deck) backdrop-blur-[10px] flex gap-4.5 py-3 px-5 pb-3.5 min-w-0 panel-anim">
				<Timeline
					from={windowStart}
					to={now}
					at={at}
					live={live}
					playing={playing}
					events={events}
					bands={bands}
					legend={
						selected
							? `status band · ${selected.name} · now ${statuses[selected.id]}`
							: "fleet alert events · drag to scrub, or replay the last 12 hours"
					}
					onScrub={scrub}
					onTogglePlay={togglePlay}
				/>
				{alertObj && series && (
					<div className="cmd-telemetry shrink-0 basis-67 border-l border-(--edge) pl-4.5 min-w-0">
						<div className="flex justify-between gap-2.5 text-xs mb-1">
							<span className="text-primary font-mono text-[11.5px]">
								{alertObj.asset_id}
							</span>
							<span className="mono muted text-[11px]">
								{humanizeKind(alertObj.kind)}
							</span>
						</div>
						<Sparkline
							series={series}
							now={at}
							height={78}
							color={STATUS_COLOR[severityStatus(alertObj.severity)]}
						/>
					</div>
				)}
			</footer>
		</div>
	);
}
