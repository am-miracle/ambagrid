import gsap from "gsap";

export { gsap };

export const prefersReducedMotion = () =>
	typeof window !== "undefined" &&
	window.matchMedia("(prefers-reduced-motion: reduce)").matches;

// Shared easing so every live value moves with the same character.
export const TICK_EASE = "back.out(1.2)";
export const TICK_DURATION = 0.6;
