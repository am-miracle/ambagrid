// Port of services/api-go/internal/page cursor encoding, so mock cursors are
// byte-identical to the ones the API issues.

const VERSION = "v1";
const SEPARATOR = "\x1f";

const escapeField = (field: string) =>
	field.replaceAll("\\", "\\\\").replaceAll(SEPARATOR, "\\u");

const unescapeField = (field: string) =>
	field.replace(/\\(u|\\)/g, (_, c: string) => (c === "u" ? SEPARATOR : "\\"));

const toBase64Url = (text: string) =>
	btoa(String.fromCharCode(...new TextEncoder().encode(text)))
		.replaceAll("+", "-")
		.replaceAll("/", "_")
		.replace(/=+$/, "");

const fromBase64Url = (token: string) => {
	const binary = atob(token.replaceAll("-", "+").replaceAll("_", "/"));
	return new TextDecoder().decode(
		Uint8Array.from(binary, (c) => c.charCodeAt(0)),
	);
};

export function encodeCursor(...fields: string[]): string {
	return toBase64Url([VERSION, ...fields.map(escapeField)].join(SEPARATOR));
}

export function decodeCursor(
	token: string,
	wantFields: number,
): string[] | null {
	try {
		const parts = fromBase64Url(token).split(SEPARATOR);
		if (parts.length !== wantFields + 1 || parts[0] !== VERSION) return null;
		return parts.slice(1).map(unescapeField);
	} catch {
		return null;
	}
}
