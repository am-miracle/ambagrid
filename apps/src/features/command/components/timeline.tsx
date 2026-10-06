import type { PointerEvent as ReactPointerEvent } from "react";
import { useRef } from "react";
import { formatDuration } from "#/lib/format";
import {
	type FleetEvent,
	formatClock,
	STATUS_COLOR,
	type Status,
} from "../model";

export function Timeline({
	from,
	to,
	at,
	live,
	playing,
	events,
	bands,
	legend,
	onScrub,
	onTogglePlay,
}: {
	from: number;
	to: number;
	at: number;
	live: boolean;
	playing: boolean;
	events: FleetEvent[];
	bands: { from: number; to: number; status: Status }[] | null;
	legend: string;
	onScrub: (t: number) => void;
	onTogglePlay: () => void;
}) {
	const ref = useRef<HTMLDivElement | null>(null);
	const span = to - from;
	const pct = (t: number) => ((t - from) / span) * 100;

	const toTime = (clientX: number) => {
		const r = ref.current?.getBoundingClientRect();
		if (!r) return to;
		const f = Math.min(1, Math.max(0, (clientX - r.left) / r.width));
		return from + f * span;
	};

	const drag = (e: ReactPointerEvent<HTMLDivElement>) => {
		e.currentTarget.setPointerCapture(e.pointerId);
		onScrub(toTime(e.clientX));
		const move = (ev: PointerEvent) => onScrub(toTime(ev.clientX));
		const up = () => {
			window.removeEventListener("pointermove", move);
			window.removeEventListener("pointerup", up);
		};
		window.addEventListener("pointermove", move);
		window.addEventListener("pointerup", up);
	};

	const hours: number[] = [];
	const hour = 60 * 60 * 1000;
	for (let t = Math.ceil(from / hour) * hour; t <= to; t += hour) hours.push(t);

	return (
		<div className="flex-1 min-w-0 flex flex-col">
			<div className="flex items-center gap-3 mb-2">
				<button
					type="button"
					className="w-7 h-7 border border-border text-primary grid place-items-center flex-none hover:border-primary hover:bg-accent"
					onClick={onTogglePlay}
					aria-label={playing ? "Pause replay" : "Play replay"}
				>
					{playing ? (
						<svg width="12" height="12" viewBox="0 0 12 12" aria-hidden="true">
							<rect x="1.5" y="1" width="3" height="10" fill="currentColor" />
							<rect x="7.5" y="1" width="3" height="10" fill="currentColor" />
						</svg>
					) : (
						<svg width="12" height="12" viewBox="0 0 12 12" aria-hidden="true">
							<path d="M2 1l9 5-9 5z" fill="currentColor" />
						</svg>
					)}
				</button>
				<div className="flex items-baseline gap-[9px] mono">
					<b className="text-[17px] font-medium">{formatClock(at)}</b>
					<span className="text-[11px] text-muted-foreground">
						{live ? "live edge" : `T-${formatDuration(to - at)}`}
					</span>
				</div>
			</div>

			<div
				className="relative flex-1 min-h-[52px] border-y border-(--edge) cursor-ew-resize touch-none bg-gradient-to-b from-[rgba(143,246,255,0.02)] to-transparent"
				ref={ref}
				onPointerDown={drag}
				role="slider"
				tabIndex={0}
				aria-label="Replay time"
				aria-valuemin={from}
				aria-valuemax={to}
				aria-valuenow={at}
				aria-valuetext={formatClock(at)}
				onKeyDown={(e) => {
					const step = e.shiftKey ? hour : 5 * 60 * 1000;
					if (e.key === "ArrowLeft") onScrub(Math.max(from, at - step));
					if (e.key === "ArrowRight") onScrub(Math.min(to, at + step));
					if (e.key === "End") onScrub(to);
				}}
			>
				{bands?.map((b) => (
					<div
						key={b.from}
						className="absolute top-0 bottom-0 pointer-events-none"
						style={{
							left: `${pct(b.from)}%`,
							width: `${pct(b.to) - pct(b.from)}%`,
							background: `linear-gradient(180deg, ${STATUS_COLOR[b.status]}44, ${STATUS_COLOR[b.status]}12)`,
							borderTop: `1.5px solid ${STATUS_COLOR[b.status]}`,
						}}
					/>
				))}
				{hours.map((h) => (
					<div
						key={h}
						className="absolute top-0 bottom-0 border-l border-(--edge) pointer-events-none"
						style={{ left: `${pct(h)}%` }}
					>
						<span className="absolute bottom-[2px] left-1 text-[9.5px] text-muted-foreground mono">
							{formatClock(h)}
						</span>
					</div>
				))}
				{events.map((e) => (
					<div
						key={`${e.at}-${e.siteId}-${e.text}`}
						className={`tl-ev absolute top-2 w-[2px] h-[22px] -translate-x-px${e.at <= at ? " opacity-100" : " opacity-35"}`}
						style={{
							left: `${pct(e.at)}%`,
							background: STATUS_COLOR[e.status],
						}}
					>
						<div className="tl-ev-tip absolute bottom-[30px] -left-2 w-max max-w-[260px] bg-[rgba(6,12,18,0.96)] border border-border px-[9px] py-[5px] text-[11.5px] z-5 text-foreground">
							<span className="mono text-[10.5px] text-muted-foreground mr-[5px]">
								{formatClock(e.at)}
							</span>{" "}
							{e.text}
						</div>
					</div>
				))}
				<div
					className="absolute top-0 bottom-0 left-0 bg-[rgba(143,246,255,0.05)] pointer-events-none"
					style={{ width: `${pct(at)}%` }}
				/>
				<div
					className="absolute top-0 bottom-0 w-px bg-primary pointer-events-none shadow-[0_0_12px_var(--cyan)]"
					style={{ left: `${pct(at)}%` }}
				>
					<span className="absolute -top-1 -left-1 w-[9px] h-[9px] bg-primary rotate-45" />
				</div>
			</div>

			<div className="text-[10.5px] text-muted-foreground mt-[6px] mono">
				{legend}
			</div>
		</div>
	);
}
