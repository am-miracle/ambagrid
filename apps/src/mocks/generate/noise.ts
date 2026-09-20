// Deterministic noise: the same (key, time) always yields the same value, so
// latest state and historical readings computed separately agree.

function hash(text: string): number {
	let h = 2166136261;
	for (let i = 0; i < text.length; i++) {
		h ^= text.charCodeAt(i);
		h = Math.imul(h, 16777619);
	}
	return h >>> 0;
}

function lattice(seed: number, index: number): number {
	let x = (seed ^ Math.imul(index, 0x9e3779b1)) >>> 0;
	x = Math.imul(x ^ (x >>> 16), 0x85ebca6b);
	x = Math.imul(x ^ (x >>> 13), 0xc2b2ae35);
	return ((x ^ (x >>> 16)) >>> 0) / 0xffffffff;
}

// Smooth value noise in [0, 1] with features roughly periodMs apart.
export function smoothNoise(
	key: string,
	timeMs: number,
	periodMs: number,
): number {
	const seed = hash(key);
	const position = timeMs / periodMs;
	const index = Math.floor(position);
	const f = position - index;
	const t = f * f * (3 - 2 * f);
	return lattice(seed, index) * (1 - t) + lattice(seed, index + 1) * t;
}

export function seededRandom(key: string): () => number {
	let state = hash(key) || 1;
	return () => {
		state ^= state << 13;
		state ^= state >>> 17;
		state ^= state << 5;
		return (state >>> 0) / 0xffffffff;
	};
}

export function seededUuid(key: string): string {
	const next = seededRandom(key);
	const hex = Array.from({ length: 32 }, () =>
		Math.floor(next() * 16).toString(16),
	);
	hex[12] = "4";
	hex[16] = ((Number.parseInt(hex[16], 16) & 0x3) | 0x8).toString(16);
	const s = hex.join("");
	return `${s.slice(0, 8)}-${s.slice(8, 12)}-${s.slice(12, 16)}-${s.slice(16, 20)}-${s.slice(20)}`;
}
