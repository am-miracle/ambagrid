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
		<div className="tl">
			<div className="tl-controls">
				<button
					type="button"
					className="play"
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
				<div className="tl-readout mono">
					<b>{formatClock(at)}</b>
					<span>{live ? "live edge" : `T-${formatDuration(to - at)}`}</span>
				</div>
			</div>

			<div
				className="tl-track"
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
						className="tl-band"
						style={{
							left: `${pct(b.from)}%`,
							width: `${pct(b.to) - pct(b.from)}%`,
							background: `linear-gradient(180deg, ${STATUS_COLOR[b.status]}44, ${STATUS_COLOR[b.status]}12)`,
							borderTop: `1.5px solid ${STATUS_COLOR[b.status]}`,
						}}
					/>
				))}
				{hours.map((h) => (
					<div key={h} className="tl-tick" style={{ left: `${pct(h)}%` }}>
						<span className="mono">{formatClock(h)}</span>
					</div>
				))}
				{events.map((e) => (
					<div
						key={`${e.at}-${e.siteId}-${e.text}`}
						className={`tl-ev${e.at <= at ? " past" : ""}`}
						style={{
							left: `${pct(e.at)}%`,
							background: STATUS_COLOR[e.status],
						}}
					>
						<div className="tl-ev-tip">
							<span className="mono">{formatClock(e.at)}</span> {e.text}
						</div>
					</div>
				))}
				<div className="tl-fill" style={{ width: `${pct(at)}%` }} />
				<div className="tl-head" style={{ left: `${pct(at)}%` }}>
					<span className="tl-head-dot" />
				</div>
			</div>

			<div className="tl-legend mono">{legend}</div>
		</div>
	);
}
