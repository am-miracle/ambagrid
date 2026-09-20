import { useEffect, useId, useRef, useState } from "react";
import { gsap, prefersReducedMotion } from "#/lib/motion";
import { formatClock, STATUS_COLOR, type Status } from "./model";

// Writes textContent directly so ticking values never re-render React.
export function AnimatedNumber({
	value,
	decimals = 0,
}: {
	value: number;
	decimals?: number;
}) {
	const ref = useRef<HTMLSpanElement | null>(null);
	const proxy = useRef({ v: value });
	useEffect(() => {
		const o = proxy.current;
		const write = () => {
			if (ref.current) ref.current.textContent = o.v.toFixed(decimals);
		};
		if (prefersReducedMotion()) {
			o.v = value;
			write();
			return;
		}
		const tween = gsap.to(o, {
			v: value,
			duration: 0.7,
			ease: "power2.out",
			onUpdate: write,
		});
		return () => {
			tween.kill();
		};
	}, [value, decimals]);
	return <span ref={ref}>{value.toFixed(decimals)}</span>;
}

export interface Series {
	label: string;
	unit: string;
	threshold?: number;
	points: [number, number][];
}

export function Sparkline({
	series,
	now,
	height = 96,
	color = STATUS_COLOR.critical,
}: {
	series: Series;
	now: number;
	height?: number;
	color?: string;
}) {
	const uid = useId();
	const pts = series.points.filter((p) => p[0] <= now);
	if (pts.length < 2) {
		return <div className="spark-empty">Waiting for telemetry</div>;
	}
	const xs = series.points.map((p) => p[0]);
	const ys = series.points.map((p) => p[1]);
	const x0 = Math.min(...xs);
	const x1 = Math.max(...xs);
	const y0 = Math.min(...ys) - 4;
	const y1 = Math.max(...ys, series.threshold ?? Number.NEGATIVE_INFINITY) + 6;
	const W = 100;
	const H = 100;
	const sx = (v: number) => ((v - x0) / (x1 - x0 || 1)) * W;
	const sy = (v: number) => H - ((v - y0) / (y1 - y0 || 1)) * H;
	const d = pts
		.map(
			(p, i) => `${i ? "L" : "M"}${sx(p[0]).toFixed(2)} ${sy(p[1]).toFixed(2)}`,
		)
		.join(" ");
	const area = `${d} L${sx(pts[pts.length - 1][0]).toFixed(2)} ${H} L${sx(pts[0][0]).toFixed(2)} ${H} Z`;
	const threshold = series.threshold;
	const breach =
		threshold !== undefined ? pts.find((p) => p[1] >= threshold) : undefined;
	const last = pts[pts.length - 1];

	return (
		<div className="spark" style={{ height }}>
			<svg
				viewBox={`0 0 ${W} ${H}`}
				preserveAspectRatio="none"
				aria-hidden="true"
			>
				<defs>
					<linearGradient id={`g${uid}`} x1="0" y1="0" x2="0" y2="1">
						<stop offset="0%" stopColor={color} stopOpacity="0.34" />
						<stop offset="100%" stopColor={color} stopOpacity="0" />
					</linearGradient>
				</defs>
				<path d={area} fill={`url(#g${uid})`} />
				{threshold !== undefined && (
					<line
						x1="0"
						x2={W}
						y1={sy(threshold)}
						y2={sy(threshold)}
						stroke={STATUS_COLOR.warning}
						strokeWidth="0.5"
						strokeDasharray="2 2"
						opacity="0.8"
						vectorEffect="non-scaling-stroke"
					/>
				)}
				<path
					d={d}
					fill="none"
					stroke={color}
					strokeWidth="1.4"
					vectorEffect="non-scaling-stroke"
				/>
				{breach && (
					<line
						x1={sx(breach[0])}
						x2={sx(breach[0])}
						y1="0"
						y2={H}
						stroke={color}
						strokeWidth="0.6"
						strokeDasharray="3 3"
						opacity="0.6"
						vectorEffect="non-scaling-stroke"
					/>
				)}
				<circle
					cx={sx(last[0])}
					cy={sy(last[1])}
					r="1.6"
					fill="#fff"
					vectorEffect="non-scaling-stroke"
				/>
			</svg>
			<div className="spark-meta">
				<span>{series.label}</span>
				<span className="mono">
					{last[1].toFixed(1)} {series.unit}
				</span>
			</div>
			{threshold !== undefined && (
				<div className="spark-note">
					{breach
						? `Reached the ${threshold}${series.unit} limit at ${formatClock(breach[0])}`
						: `Below the ${threshold}${series.unit} limit`}
				</div>
			)}
		</div>
	);
}

export function StatusDot({
	status,
	size = 8,
}: {
	status: Status;
	size?: number;
}) {
	return (
		<span
			className="sdot"
			style={{
				width: size,
				height: size,
				background: STATUS_COLOR[status],
				boxShadow: `0 0 ${size}px ${STATUS_COLOR[status]}`,
			}}
		/>
	);
}

const BOOT_NODES = Array.from({ length: 12 }, (_, i) => {
	const a = (i / 12) * Math.PI * 2;
	return { id: `n${i}`, x: 90 + Math.cos(a) * 62, y: 90 + Math.sin(a) * 62 };
});

export function Boot({
	lines,
	onDone,
}: {
	lines: string[];
	onDone: () => void;
}) {
	const ref = useRef<HTMLDivElement | null>(null);
	const onDoneRef = useRef(onDone);
	onDoneRef.current = onDone;
	const [line, setLine] = useState(0);
	const lineCount = lines.length;

	useEffect(() => {
		if (prefersReducedMotion()) {
			onDoneRef.current();
			return;
		}
		const id = setInterval(
			() => setLine((l) => Math.min(l + 1, lineCount - 1)),
			420,
		);
		const scope = ref.current;
		const tl = gsap.timeline({ delay: 0.2 });
		tl.fromTo(
			scope?.querySelectorAll(".boot-node") ?? [],
			{ scale: 0, opacity: 0 },
			{
				scale: 1,
				opacity: 1,
				duration: 0.5,
				stagger: 0.045,
				ease: "back.out(2)",
				transformOrigin: "center",
			},
		)
			.fromTo(
				scope?.querySelectorAll(".boot-link") ?? [],
				{ strokeDashoffset: 120 },
				{
					strokeDashoffset: 0,
					duration: 0.7,
					stagger: 0.03,
					ease: "power2.out",
				},
				"-=0.5",
			)
			.to(scope, {
				opacity: 0,
				duration: 0.55,
				delay: 0.5,
				ease: "power2.inOut",
				onComplete: () => onDoneRef.current(),
			});
		return () => {
			clearInterval(id);
			tl.kill();
		};
	}, [lineCount]);

	return (
		<div className="boot" ref={ref}>
			<svg width="180" height="180" viewBox="0 0 180 180" aria-hidden="true">
				{BOOT_NODES.map((n) => (
					<line
						key={`l${n.id}`}
						className="boot-link"
						x1="90"
						y1="90"
						x2={n.x}
						y2={n.y}
						stroke="#2C7F92"
						strokeWidth="1"
						strokeDasharray="120"
					/>
				))}
				{BOOT_NODES.map((n, i) => (
					<circle
						key={n.id}
						className="boot-node"
						cx={n.x}
						cy={n.y}
						r="4"
						fill={
							i === 3
								? STATUS_COLOR.critical
								: i === 7
									? STATUS_COLOR.warning
									: STATUS_COLOR.healthy
						}
					/>
				))}
				<circle
					className="boot-node"
					cx="90"
					cy="90"
					r="9"
					fill="none"
					stroke="#8FF6FF"
					strokeWidth="1.5"
				/>
				<circle className="boot-node" cx="90" cy="90" r="3" fill="#8FF6FF" />
			</svg>
			<div className="boot-title">AmbaGrid Control</div>
			<div className="boot-line mono" aria-live="polite">
				{lines[line]}
			</div>
		</div>
	);
}
