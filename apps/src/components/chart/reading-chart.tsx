import { useId, useMemo, useState } from "react";
import type { ReadingPoint } from "#/api/types";
import { formatDateTime, formatValue } from "#/lib/format";

const WIDTH = 640;
const HEIGHT = 220;
const PAD = { top: 12, right: 12, bottom: 24, left: 44 };

function niceTicks(min: number, max: number, count = 4): number[] {
	if (min === max) return [min];
	const span = max - min;
	const step = 10 ** Math.floor(Math.log10(span / count));
	const residual = span / count / step;
	const niceStep =
		residual > 5
			? step * 10
			: residual > 2
				? step * 5
				: residual > 1
					? step * 2
					: step;
	const start = Math.ceil(min / niceStep) * niceStep;
	const ticks: number[] = [];
	for (let v = start; v <= max; v += niceStep) ticks.push(v);
	return ticks;
}

// Single-series line chart per the dataviz mark spec: 2px line, ~10% area
// wash, hairline recessive gridlines, an end marker, and a crosshair tooltip
// that lists the value at the nearest point. A single series needs no legend
// box — the chart title names it.
export function ReadingChart({
	points,
	unit,
	label,
}: {
	points: ReadingPoint[];
	unit: string;
	label: string;
}) {
	const gradientId = useId();
	const [hoverIndex, setHoverIndex] = useState<number | null>(null);

	const { xScale, yScale, linePath, areaPath, yTicks, values, times } =
		useMemo(() => {
			const innerWidth = WIDTH - PAD.left - PAD.right;
			const innerHeight = HEIGHT - PAD.top - PAD.bottom;
			const values = points.map((p) => p.value);
			const times = points.map((p) => Date.parse(p.time));
			const minV = values.length ? Math.min(...values) : 0;
			const maxV = values.length ? Math.max(...values) : 1;
			const pad = (maxV - minV) * 0.12 || 1;
			const lo = minV - pad;
			const hi = maxV + pad;
			const minT = times[0] ?? 0;
			const maxT = times[times.length - 1] ?? 1;

			const xScale = (t: number) =>
				PAD.left +
				(maxT === minT ? 0 : ((t - minT) / (maxT - minT)) * innerWidth);
			const yScale = (v: number) =>
				PAD.top + innerHeight - ((v - lo) / (hi - lo)) * innerHeight;

			const linePath = points
				.map(
					(p, i) =>
						`${i === 0 ? "M" : "L"}${xScale(Date.parse(p.time))} ${yScale(p.value)}`,
				)
				.join(" ");
			const areaPath = points.length
				? `${linePath} L${xScale(maxT)} ${PAD.top + innerHeight} L${xScale(minT)} ${PAD.top + innerHeight} Z`
				: "";

			return {
				xScale,
				yScale,
				linePath,
				areaPath,
				yTicks: niceTicks(lo, hi),
				values,
				times,
			};
		}, [points]);

	if (points.length === 0) {
		return (
			<div className="flex h-55 items-center justify-center text-sm text-muted-foreground">
				No readings in this window.
			</div>
		);
	}

	const hovered = hoverIndex !== null ? points[hoverIndex] : null;

	return (
		<div className="relative">
			<svg
				viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
				className="w-full"
				role="img"
				aria-label={`${label} over the selected window`}
				onPointerMove={(e) => {
					const rect = e.currentTarget.getBoundingClientRect();
					const x = ((e.clientX - rect.left) / rect.width) * WIDTH;
					let nearest = 0;
					let best = Number.POSITIVE_INFINITY;
					times.forEach((t, i) => {
						const d = Math.abs(xScale(t) - x);
						if (d < best) {
							best = d;
							nearest = i;
						}
					});
					setHoverIndex(nearest);
				}}
				onPointerLeave={() => setHoverIndex(null)}
			>
				<defs>
					<linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
						<stop offset="0%" stopColor="var(--chart-1)" stopOpacity={0.1} />
						<stop offset="100%" stopColor="var(--chart-1)" stopOpacity={0} />
					</linearGradient>
				</defs>

				{yTicks.map((tick) => (
					<g key={tick}>
						<line
							x1={PAD.left}
							x2={WIDTH - PAD.right}
							y1={yScale(tick)}
							y2={yScale(tick)}
							stroke="var(--border)"
							strokeWidth={1}
						/>
						<text
							x={PAD.left - 8}
							y={yScale(tick)}
							textAnchor="end"
							dy="0.32em"
							className="fill-muted-foreground text-[10px]"
						>
							{formatValue(tick, 0)}
						</text>
					</g>
				))}

				<path d={areaPath} fill={`url(#${gradientId})`} />
				<path
					d={linePath}
					fill="none"
					stroke="var(--chart-1)"
					strokeWidth={2}
					strokeLinejoin="round"
					strokeLinecap="round"
				/>

				<circle
					cx={xScale(times[times.length - 1])}
					cy={yScale(values[values.length - 1])}
					r={4}
					fill="var(--chart-1)"
					stroke="var(--card)"
					strokeWidth={2}
				/>

				{hovered && (
					<line
						x1={xScale(Date.parse(hovered.time))}
						x2={xScale(Date.parse(hovered.time))}
						y1={PAD.top}
						y2={HEIGHT - PAD.bottom}
						stroke="var(--muted-foreground)"
						strokeWidth={1}
						strokeDasharray="2 3"
					/>
				)}
				{hovered && (
					<circle
						cx={xScale(Date.parse(hovered.time))}
						cy={yScale(hovered.value)}
						r={4}
						fill="var(--chart-1)"
						stroke="var(--card)"
						strokeWidth={2}
					/>
				)}
			</svg>

			{hovered && (
				<div
					className="pointer-events-none absolute top-2 rounded-md border border-border bg-popover px-2 py-1.5 text-xs shadow-md"
					style={{
						left: `${(xScale(Date.parse(hovered.time)) / WIDTH) * 100}%`,
						transform:
							xScale(Date.parse(hovered.time)) > WIDTH * 0.7
								? "translateX(-100%)"
								: undefined,
					}}
				>
					<div className="font-heading tabular text-foreground">
						{formatValue(hovered.value, 1)} {unit}
					</div>
					<div className="text-muted-foreground">
						{formatDateTime(hovered.time)}
					</div>
				</div>
			)}
		</div>
	);
}
