// Stylised Nigeria border, [lon, lat]. Simplified for a command surface, not
// cartography. Sites outside this outline still project, just off the slab.
export const NIGERIA: [number, number][] = [
	[2.72, 6.37],
	[2.76, 7.1],
	[2.8, 7.88],
	[3.32, 8.6],
	[3.62, 9.02],
	[3.5, 9.82],
	[3.96, 10.56],
	[3.62, 11.72],
	[4.12, 12.34],
	[4.52, 13.06],
	[5.42, 13.86],
	[6.82, 13.34],
	[8.42, 13.1],
	[9.62, 12.86],
	[10.92, 13.32],
	[12.32, 13.14],
	[13.12, 13.54],
	[14.2, 13.1],
	[14.66, 12.3],
	[14.2, 11.66],
	[13.66, 11.06],
	[13.4, 10.1],
	[12.9, 9.66],
	[12.72, 8.82],
	[11.78, 7.12],
	[11.42, 6.62],
	[10.62, 6.9],
	[9.82, 6.46],
	[9.02, 5.92],
	[8.92, 4.86],
	[8.52, 4.56],
	[7.82, 4.54],
	[7.1, 4.36],
	[6.4, 4.3],
	[5.6, 4.62],
	[5.12, 5.62],
	[4.32, 6.2],
	[3.4, 6.42],
];

const GEO_CENTER: [number, number] = [8.6, 9.0];
const GEO_SCALE = 62;

export interface World {
	x: number;
	y: number;
	z: number;
}

export function toWorld(lon: number, lat: number, z = 0): World {
	return {
		x: (lon - GEO_CENTER[0]) * GEO_SCALE,
		y: (lat - GEO_CENTER[1]) * GEO_SCALE,
		z,
	};
}

export interface Camera {
	tx: number;
	ty: number;
	dist: number;
	yaw: number;
	pitch: number;
	focal: number;
}

export interface Projected {
	x: number;
	y: number;
	depth: number;
	scale: number;
}

// Camera sits south of the target, elevated by pitch.
export function project(
	p: World,
	cam: Camera,
	cx: number,
	cy: number,
): Projected {
	const dx = p.x - cam.tx;
	const dy = p.y - cam.ty;
	const cosYaw = Math.cos(cam.yaw);
	const sinYaw = Math.sin(cam.yaw);
	const rx = dx * cosYaw - dy * sinYaw;
	const ry = dx * sinYaw + dy * cosYaw;
	const cosPitch = Math.cos(cam.pitch);
	const sinPitch = Math.sin(cam.pitch);
	const depth = cam.dist + ry * cosPitch - p.z * sinPitch;
	const up = -(ry * sinPitch + p.z * cosPitch);
	const s = cam.focal / Math.max(depth, 90);
	return { x: cx + rx * s, y: cy + up * s, depth, scale: s };
}
