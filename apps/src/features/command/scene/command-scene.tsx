import { useEffect, useRef } from "react";
import { gsap, prefersReducedMotion } from "#/lib/motion";
import { STATUS_COLOR, type Status } from "../model";
import { type Camera, NIGERIA, project, toWorld, type World } from "./geo";

// Hand-rolled perspective projection on a 2D canvas: no WebGL, so it runs on
// the low-end laptops field operators actually have.

export interface SceneSite {
	id: string;
	name: string;
	region: string;
	lat: number;
	lon: number;
	status: Status;
	loadKw: number;
	loadRatio: number;
	soc: number | null;
	meterCount: number;
}

export interface CameraApi {
	focusSite: (id: string, tight?: boolean) => void;
	reset: () => void;
}

interface SceneProps {
	sites: SceneSite[];
	links: [string, string][];
	selectedId: string | null;
	focusAssetId: string | null;
	focusSiteId: string | null;
	focusMode: boolean;
	visible: Record<Status, boolean>;
	onSelect: (id: string | null) => void;
	onHover: (id: string | null, x: number, y: number) => void;
	registerCamera: (api: CameraApi) => void;
}

interface NodeHit {
	id: string;
	x: number;
	y: number;
}

const SELECT = "#8FF6FF";
const HOME: Omit<Camera, "focal"> = {
	tx: 0,
	ty: 0,
	dist: 1240,
	yaw: 0,
	pitch: 0.92,
};
const LABEL_FONT = "600 12.5px 'Barlow Semi Condensed', system-ui, sans-serif";
const MONO_FONT = "500 10.5px 'IBM Plex Mono', monospace";

function rgba(hex: string, a: number) {
	const n = Number.parseInt(hex.slice(1), 16);
	return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${a})`;
}

export function CommandScene(props: SceneProps) {
	const canvasRef = useRef<HTMLCanvasElement | null>(null);
	const wrapRef = useRef<HTMLDivElement | null>(null);
	const propsRef = useRef(props);
	propsRef.current = props;

	const camRef = useRef<Camera>({ ...HOME, focal: 900 });
	const hitsRef = useRef<NodeHit[]>([]);
	const dimRef = useRef<Record<string, number>>({});
	const bootRef = useRef({ t: 0 });

	useEffect(() => {
		const api: CameraApi = {
			focusSite: (id, tight = false) => {
				const s = propsRef.current.sites.find((x) => x.id === id);
				if (!s) return;
				const w = toWorld(s.lon, s.lat);
				gsap.to(camRef.current, {
					tx: w.x,
					ty: w.y - 8,
					dist: tight ? 540 : 700,
					pitch: tight ? 0.92 : 1.0,
					duration: 1.15,
					ease: "power3.inOut",
					overwrite: true,
				});
			},
			reset: () => {
				gsap.to(camRef.current, {
					...HOME,
					duration: 1.0,
					ease: "power3.inOut",
					overwrite: true,
				});
			},
		};
		propsRef.current.registerCamera(api);
		gsap.fromTo(
			camRef.current,
			{ dist: 2600, pitch: 1.32 },
			{ dist: HOME.dist, pitch: HOME.pitch, duration: 2.1, ease: "power3.out" },
		);
		gsap.fromTo(
			bootRef.current,
			{ t: 0 },
			{ t: 1, duration: 1.9, ease: "power2.out", delay: 0.15 },
		);
	}, []);

	useEffect(() => {
		const canvas = canvasRef.current;
		const wrap = wrapRef.current;
		const ctx = canvas?.getContext("2d");
		if (!canvas || !wrap || !ctx) return;
		let raf = 0;
		let w = 0;
		let h = 0;
		const still = prefersReducedMotion();

		const ro = new ResizeObserver(() => {
			const dpr = Math.min(window.devicePixelRatio || 1, 2);
			w = wrap.clientWidth;
			h = wrap.clientHeight;
			canvas.width = Math.max(1, Math.floor(w * dpr));
			canvas.height = Math.max(1, Math.floor(h * dpr));
			canvas.style.width = `${w}px`;
			canvas.style.height = `${h}px`;
			ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
		});
		ro.observe(wrap);

		const draw = (time: number) => {
			raf = requestAnimationFrame(draw);
			const p = propsRef.current;
			const cam = camRef.current;
			const boot = bootRef.current.t;
			if (!w || !h) return;
			const cx = w / 2;
			const cy = h / 2 + 20;
			cam.focal = Math.max(700, Math.min(2100, w * 1.28));
			const t = still ? 0 : time / 1000;
			const drift = Math.sin(t * 0.09) * 0.022;
			const live: Camera = { ...cam, yaw: cam.yaw + drift };
			const pr = (q: World) => project(q, live, cx, cy);
			const byId = new Map(p.sites.map((s) => [s.id, s]));
			const maxMeters = Math.max(1, ...p.sites.map((s) => s.meterCount));

			ctx.clearRect(0, 0, w, h);

			// Terrain slab.
			const top = NIGERIA.map(([lon, lat]) => pr(toWorld(lon, lat, 0)));
			const bot = NIGERIA.map(([lon, lat]) => pr(toWorld(lon, lat, -34)));

			ctx.save();
			ctx.globalAlpha = boot;
			for (let i = 0; i < top.length; i++) {
				const j = (i + 1) % top.length;
				ctx.beginPath();
				ctx.moveTo(top[i].x, top[i].y);
				ctx.lineTo(top[j].x, top[j].y);
				ctx.lineTo(bot[j].x, bot[j].y);
				ctx.lineTo(bot[i].x, bot[i].y);
				ctx.closePath();
				const g = ctx.createLinearGradient(
					top[i].x,
					top[i].y,
					bot[i].x,
					bot[i].y,
				);
				g.addColorStop(0, "#101c27");
				g.addColorStop(1, "#05080d");
				ctx.fillStyle = g;
				ctx.fill();
				ctx.strokeStyle = "rgba(80,140,170,0.10)";
				ctx.lineWidth = 1;
				ctx.stroke();
			}

			const trace = () => {
				ctx.beginPath();
				top.forEach((pt, i) => {
					if (i) ctx.lineTo(pt.x, pt.y);
					else ctx.moveTo(pt.x, pt.y);
				});
				ctx.closePath();
			};
			const tg = ctx.createLinearGradient(0, cy - 320, 0, cy + 320);
			tg.addColorStop(0, "#0a141d");
			tg.addColorStop(1, "#070d14");
			ctx.fillStyle = tg;
			trace();
			ctx.fill();

			// Graticule clipped to the territory.
			ctx.save();
			trace();
			ctx.clip();
			ctx.lineWidth = 1;
			ctx.strokeStyle = "rgba(94,168,196,0.085)";
			const polyline = (points: World[]) => {
				ctx.beginPath();
				points.forEach((q, i) => {
					const s = pr(q);
					if (i) ctx.lineTo(s.x, s.y);
					else ctx.moveTo(s.x, s.y);
				});
			};
			const range = (a: number, b: number, step: number) => {
				const out: number[] = [];
				for (let v = a; v <= b; v += step) out.push(v);
				return out;
			};
			for (const lat of range(4, 14, 1)) {
				polyline(range(2, 15, 0.5).map((lon) => toWorld(lon, lat, 0)));
				ctx.stroke();
			}
			for (const lon of range(2, 15, 1)) {
				polyline(range(4, 14, 0.5).map((lat) => toWorld(lon, lat, 0)));
				ctx.stroke();
			}

			// A poll sweep moving south to north, echoing the 5s telemetry tick.
			const sweepLat = 3.6 + ((t % 7) / 7) * 11;
			const band = ctx.createLinearGradient(
				0,
				pr(toWorld(8.6, sweepLat + 1.6, 0)).y,
				0,
				pr(toWorld(8.6, sweepLat - 0.2, 0)).y,
			);
			band.addColorStop(0, "rgba(88,214,236,0)");
			band.addColorStop(1, "rgba(88,214,236,0.11)");
			ctx.fillStyle = band;
			trace();
			ctx.fill();
			polyline(range(2, 15, 0.5).map((lon) => toWorld(lon, sweepLat, 0)));
			ctx.strokeStyle = "rgba(120,232,252,0.22)";
			ctx.lineWidth = 1.2;
			ctx.stroke();
			ctx.restore();

			ctx.strokeStyle = "rgba(120,206,230,0.42)";
			ctx.lineWidth = 1.4;
			trace();
			ctx.stroke();
			ctx.restore();

			// Site projection.
			const focusOn = p.focusMode
				? (p.focusSiteId ?? p.selectedId)
				: p.selectedId;
			const nodes = p.sites
				.map((s) => {
					const height =
						16 +
						Math.min(1, s.loadRatio) * 52 * (s.status === "offline" ? 0.18 : 1);
					const base = toWorld(s.lon, s.lat, 0);
					const tip = toWorld(s.lon, s.lat, height * boot);
					return { s, base, pb: pr(base), pt: pr(tip) };
				})
				.sort((a, b) => b.pb.depth - a.pb.depth);

			for (const s of p.sites) {
				const shown = p.visible[s.status];
				let target = shown ? 1 : 0.06;
				if (p.focusMode && focusOn && s.id !== focusOn) target = 0.1;
				else if (focusOn && s.id !== focusOn && p.selectedId)
					target = shown ? 0.55 : 0.06;
				const cur = dimRef.current[s.id] ?? 0;
				dimRef.current[s.id] = cur + (target - cur) * 0.09;
			}

			// Proximity mesh with flow packets.
			ctx.save();
			ctx.lineWidth = 1;
			for (const [a, b] of p.links) {
				const A = byId.get(a);
				const B = byId.get(b);
				if (!A || !B) continue;
				const alpha =
					Math.min(dimRef.current[a] ?? 0, dimRef.current[b] ?? 0) * boot;
				if (alpha < 0.02) continue;
				const statuses = [A.status, B.status];
				const dead = statuses.includes("offline");
				const sev: Status = statuses.includes("critical")
					? "critical"
					: statuses.includes("warning")
						? "warning"
						: "healthy";
				const col = dead ? STATUS_COLOR.offline : STATUS_COLOR[sev];
				const along = (f: number) =>
					pr(
						toWorld(
							A.lon + (B.lon - A.lon) * f,
							A.lat + (B.lat - A.lat) * f,
							Math.sin(Math.PI * f) * 16,
						),
					);
				ctx.beginPath();
				for (let k = 0; k <= 16; k++) {
					const q = along(k / 16);
					if (k) ctx.lineTo(q.x, q.y);
					else ctx.moveTo(q.x, q.y);
				}
				ctx.strokeStyle = rgba(col, dead ? 0.06 * alpha : 0.14 * alpha);
				if (dead) ctx.setLineDash([3, 6]);
				ctx.stroke();
				ctx.setLineDash([]);
				if (dead) continue;
				for (let k = 0; k < 2; k++) {
					const q = along((t * 0.17 + k * 0.5 + A.lat * 0.07) % 1);
					ctx.beginPath();
					ctx.arc(q.x, q.y, 1.6, 0, Math.PI * 2);
					ctx.fillStyle = rgba(col, 0.75 * alpha);
					ctx.fill();
				}
			}
			ctx.restore();

			// Nodes.
			const hits: NodeHit[] = [];
			type Label = {
				x: number;
				y: number;
				name: string;
				sub: string;
				col: string;
				sel: boolean;
				prio: number;
				a: number;
			};
			const labels: Label[] = [];
			for (const n of nodes) {
				const alpha = (dimRef.current[n.s.id] ?? 0) * boot;
				hits.push({ id: n.s.id, x: n.pt.x, y: n.pt.y });
				if (alpha < 0.02) continue;
				const st = n.s.status;
				const col = STATUS_COLOR[st];
				const isSel = p.selectedId === n.s.id;
				const isFocus = focusOn === n.s.id;
				const sc = n.pb.scale;

				const ring = (
					radius: number,
					a: number,
					width: number,
					dash?: number[],
				) => {
					ctx.beginPath();
					for (let k = 0; k <= 32; k++) {
						const ang = (k / 32) * Math.PI * 2;
						const q = pr({
							x: n.base.x + Math.cos(ang) * radius,
							y: n.base.y + Math.sin(ang) * radius,
							z: 0,
						});
						if (k) ctx.lineTo(q.x, q.y);
						else ctx.moveTo(q.x, q.y);
					}
					ctx.closePath();
					if (dash) ctx.setLineDash(dash);
					ctx.lineWidth = width;
					ctx.strokeStyle = rgba(isSel ? SELECT : col, a * alpha);
					ctx.stroke();
					ctx.setLineDash([]);
				};

				const r0 = 9 + (n.s.meterCount / maxMeters) * 6;
				if (st === "offline") {
					ring(r0, 0.45, 1, [4, 5]);
				} else {
					ring(r0, 0.5, 1.2);
					const speed = st === "critical" ? 1.05 : st === "warning" ? 2.1 : 3.6;
					const waves = st === "healthy" ? 1 : 2;
					for (let k = 0; k < waves; k++) {
						const ph = (t / speed + k / waves) % 1;
						const amp =
							st === "critical" ? 0.85 : st === "warning" ? 0.6 : 0.24;
						ring(
							r0 + ph * (st === "healthy" ? 14 : 26),
							(1 - ph) * amp,
							st === "critical" ? 1.8 : 1.2,
						);
					}
				}

				const poolR = Math.max(8, r0 * sc * 2.4);
				const gp = ctx.createRadialGradient(
					n.pb.x,
					n.pb.y,
					0,
					n.pb.x,
					n.pb.y,
					poolR,
				);
				gp.addColorStop(0, rgba(col, 0.28 * alpha));
				gp.addColorStop(1, rgba(col, 0));
				ctx.fillStyle = gp;
				ctx.beginPath();
				ctx.arc(n.pb.x, n.pb.y, poolR, 0, Math.PI * 2);
				ctx.fill();

				const grad = ctx.createLinearGradient(n.pb.x, n.pb.y, n.pt.x, n.pt.y);
				grad.addColorStop(0, rgba(col, 0.05 * alpha));
				grad.addColorStop(
					1,
					rgba(col, (st === "offline" ? 0.35 : 0.95) * alpha),
				);
				ctx.beginPath();
				ctx.moveTo(n.pb.x, n.pb.y);
				ctx.lineTo(n.pt.x, n.pt.y);
				ctx.strokeStyle = grad;
				ctx.lineWidth = Math.max(1.2, sc * (isFocus ? 3.4 : 2.1));
				ctx.stroke();

				const capR = Math.max(2.2, sc * (isFocus ? 4.2 : 3.1));
				const cg = ctx.createRadialGradient(
					n.pt.x,
					n.pt.y,
					0,
					n.pt.x,
					n.pt.y,
					capR * 5,
				);
				cg.addColorStop(0, rgba(col, 0.55 * alpha));
				cg.addColorStop(1, rgba(col, 0));
				ctx.fillStyle = cg;
				ctx.beginPath();
				ctx.arc(n.pt.x, n.pt.y, capR * 5, 0, Math.PI * 2);
				ctx.fill();
				ctx.beginPath();
				ctx.arc(n.pt.x, n.pt.y, capR, 0, Math.PI * 2);
				ctx.fillStyle =
					st === "offline"
						? rgba(col, 0.55 * alpha)
						: rgba("#EAFDFF", 0.92 * alpha);
				ctx.fill();
				ctx.lineWidth = 1.4;
				ctx.strokeStyle = rgba(col, alpha);
				ctx.stroke();

				if (isSel) {
					const b = capR + 9;
					ctx.strokeStyle = rgba(SELECT, 0.95 * alpha);
					ctx.lineWidth = 1.4;
					for (const [sx, sy] of [
						[-1, -1],
						[1, -1],
						[1, 1],
						[-1, 1],
					]) {
						ctx.beginPath();
						ctx.moveTo(n.pt.x + sx * b, n.pt.y + sy * b - sy * 5);
						ctx.lineTo(n.pt.x + sx * b, n.pt.y + sy * b);
						ctx.lineTo(n.pt.x + sx * b - sx * 5, n.pt.y + sy * b);
						ctx.stroke();
					}
					ctx.beginPath();
					ctx.arc(
						n.pt.x,
						n.pt.y,
						b + 6 + Math.sin(t * 2) * 1.5,
						0,
						Math.PI * 2,
					);
					ctx.strokeStyle = rgba(SELECT, 0.28 * alpha);
					ctx.stroke();
				}

				if (isFocus && p.focusAssetId && p.focusMode) {
					const ang = t * 0.5;
					const orb = pr({
						x: n.base.x + Math.cos(ang) * 22,
						y: n.base.y + Math.sin(ang) * 22,
						z: 26,
					});
					ctx.beginPath();
					ctx.moveTo(n.pt.x, n.pt.y);
					ctx.lineTo(orb.x, orb.y);
					ctx.strokeStyle = rgba(col, 0.5 * alpha);
					ctx.lineWidth = 1;
					ctx.stroke();
					ctx.beginPath();
					ctx.arc(orb.x, orb.y, 4 + Math.sin(t * 4) * 1.2, 0, Math.PI * 2);
					ctx.fillStyle = rgba(col, 0.9 * alpha);
					ctx.fill();
					ctx.font = "600 11px 'Barlow Semi Condensed', system-ui, sans-serif";
					ctx.fillStyle = rgba("#EAFDFF", 0.95 * alpha);
					ctx.fillText(p.focusAssetId, orb.x + 9, orb.y + 4);
				}

				// Local energy flow once the camera is close to a selected site.
				if (isSel && live.dist < 900) {
					const sats: [string, number][] = [
						["PV", -0.7],
						["BATT", 0.5],
						["LOAD", 1.9],
						["METERS", 3.3],
					];
					for (const [label, a0] of sats) {
						const ang = a0 + t * 0.12;
						const sp = pr({
							x: n.base.x + Math.cos(ang) * 34,
							y: n.base.y + Math.sin(ang) * 34,
							z: 8,
						});
						ctx.beginPath();
						ctx.arc(sp.x, sp.y, 2.6, 0, Math.PI * 2);
						ctx.fillStyle = rgba(SELECT, 0.7 * alpha);
						ctx.fill();
						ctx.beginPath();
						ctx.moveTo(sp.x, sp.y);
						ctx.lineTo(n.pb.x, n.pb.y);
						ctx.strokeStyle = rgba(SELECT, 0.14 * alpha);
						ctx.lineWidth = 1;
						ctx.stroke();
						const f = (((t * 0.4 + a0) % 1) + 1) % 1;
						const fp = pr({
							x: n.base.x + Math.cos(ang) * 34 * (1 - f),
							y: n.base.y + Math.sin(ang) * 34 * (1 - f),
							z: 8 * (1 - f),
						});
						ctx.beginPath();
						ctx.arc(fp.x, fp.y, 1.5, 0, Math.PI * 2);
						ctx.fillStyle = rgba(SELECT, 0.8 * alpha);
						ctx.fill();
						ctx.font = "500 10px 'IBM Plex Mono', monospace";
						ctx.fillStyle = rgba("#9FC3D4", 0.8 * alpha);
						ctx.fillText(label, sp.x + 6, sp.y - 5);
					}
				}

				if (isSel || isFocus || st !== "healthy" || live.dist < 820) {
					const soc = n.s.soc === null ? "—" : `${Math.round(n.s.soc * 100)}%`;
					labels.push({
						x: n.pt.x,
						y: n.pt.y,
						name: n.s.name,
						col,
						sel: isSel,
						a: alpha,
						prio:
							(isSel || isFocus ? 100 : 0) +
							(st === "critical"
								? 40
								: st === "warning"
									? 25
									: st === "offline"
										? 12
										: 1),
						sub:
							st === "offline"
								? `${n.s.region} · dark`
								: `${n.s.region} · ${n.s.loadKw.toFixed(1)} kW · ${soc}`,
					});
				}
			}

			// Labels by priority, skipping any that would overlap.
			const placed: { x: number; y: number; w: number; h: number }[] = [];
			const overlaps = (r: { x: number; y: number; w: number; h: number }) =>
				placed.some(
					(q) =>
						!(
							r.x + r.w < q.x ||
							q.x + q.w < r.x ||
							r.y + r.h < q.y ||
							q.y + q.h < r.y
						),
				);
			labels.sort((a, b) => b.prio - a.prio);
			for (const L of labels) {
				ctx.font = LABEL_FONT;
				const nw = ctx.measureText(L.name).width;
				ctx.font = MONO_FONT;
				const sw = ctx.measureText(L.sub).width;
				const bw = Math.max(nw, sw) + 10;
				const flip = L.x + bw + 18 > w;
				let rect = {
					x: flip ? L.x - bw - 12 : L.x + 11,
					y: L.y - 22,
					w: bw,
					h: 30,
				};
				let ok = !overlaps(rect);
				for (const dy of [-32, 32, -62]) {
					if (ok) break;
					const alt = { ...rect, y: rect.y + dy };
					if (!overlaps(alt)) {
						rect = alt;
						ok = true;
					}
				}
				if (!ok) continue;
				placed.push(rect);
				ctx.fillStyle = rgba("#04080d", 0.68 * L.a);
				ctx.fillRect(rect.x - 4, rect.y, rect.w + 4, rect.h - 4);
				ctx.fillStyle = rgba(L.sel ? SELECT : "#D8E8F2", 0.97 * L.a);
				ctx.font = LABEL_FONT;
				ctx.fillText(L.name, rect.x, rect.y + 12);
				ctx.font = MONO_FONT;
				ctx.fillStyle = rgba(L.col, 0.88 * L.a);
				ctx.fillText(L.sub, rect.x, rect.y + 24);
			}
			hitsRef.current = hits;
		};

		raf = requestAnimationFrame(draw);
		return () => {
			cancelAnimationFrame(raf);
			ro.disconnect();
		};
	}, []);

	useEffect(() => {
		const canvas = canvasRef.current;
		if (!canvas) return;
		let dragging = false;
		let moved = 0;
		let lx = 0;
		let ly = 0;

		const nearest = (mx: number, my: number) => {
			let best: NodeHit | null = null;
			let bd = 26 * 26;
			for (const hit of hitsRef.current) {
				const d = (hit.x - mx) ** 2 + (hit.y - my) ** 2;
				if (d < bd) {
					bd = d;
					best = hit;
				}
			}
			return best;
		};

		const onDown = (e: PointerEvent) => {
			dragging = true;
			moved = 0;
			lx = e.clientX;
			ly = e.clientY;
			canvas.setPointerCapture(e.pointerId);
		};
		const onMove = (e: PointerEvent) => {
			const r = canvas.getBoundingClientRect();
			if (dragging) {
				const dx = e.clientX - lx;
				const dy = e.clientY - ly;
				moved += Math.abs(dx) + Math.abs(dy);
				lx = e.clientX;
				ly = e.clientY;
				gsap.killTweensOf(camRef.current);
				camRef.current.yaw -= dx * 0.004;
				camRef.current.pitch = Math.min(
					1.38,
					Math.max(0.52, camRef.current.pitch + dy * 0.004),
				);
				return;
			}
			const hit = nearest(e.clientX - r.left, e.clientY - r.top);
			canvas.style.cursor = hit ? "pointer" : "grab";
			propsRef.current.onHover(hit ? hit.id : null, e.clientX, e.clientY);
		};
		const onUp = (e: PointerEvent) => {
			const r = canvas.getBoundingClientRect();
			if (dragging && moved < 6) {
				const hit = nearest(e.clientX - r.left, e.clientY - r.top);
				propsRef.current.onSelect(hit ? hit.id : null);
			}
			dragging = false;
		};
		const onLeave = () => propsRef.current.onHover(null, 0, 0);
		const onWheel = (e: WheelEvent) => {
			e.preventDefault();
			gsap.killTweensOf(camRef.current);
			camRef.current.dist = Math.min(
				2400,
				Math.max(430, camRef.current.dist * (1 + e.deltaY * 0.0012)),
			);
		};
		canvas.addEventListener("pointerdown", onDown);
		canvas.addEventListener("pointermove", onMove);
		canvas.addEventListener("pointerup", onUp);
		canvas.addEventListener("pointerleave", onLeave);
		canvas.addEventListener("wheel", onWheel, { passive: false });
		return () => {
			canvas.removeEventListener("pointerdown", onDown);
			canvas.removeEventListener("pointermove", onMove);
			canvas.removeEventListener("pointerup", onUp);
			canvas.removeEventListener("pointerleave", onLeave);
			canvas.removeEventListener("wheel", onWheel);
		};
	}, []);

	return (
		<div className="scene" ref={wrapRef}>
			<canvas
				ref={canvasRef}
				role="img"
				aria-label="Fleet map of sites by status. The alert inbox lists the same information."
			/>
		</div>
	);
}
