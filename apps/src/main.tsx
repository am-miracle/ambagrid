import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createRouter, RouterProvider } from "@tanstack/react-router";
import ReactDOM from "react-dom/client";
import { routeTree } from "./routeTree.gen";

const queryClient = new QueryClient({
	defaultOptions: {
		queries: {
			staleTime: 2_000,
			retry: (count, error) =>
				count < 2 && !("status" in error && Number(error.status) < 500),
		},
	},
});

const router = createRouter({
	routeTree,
	defaultPreload: "intent",
	scrollRestoration: true,
});

declare module "@tanstack/react-router" {
	interface Register {
		router: typeof router;
	}
}

async function enableMocks() {
	if (import.meta.env.VITE_USE_MOCKS !== "true") return;
	const { worker } = await import("./mocks/browser");
	await worker.start({ onUnhandledRequest: "bypass" });
}

function mount(root: HTMLElement) {
	if (!root.innerHTML) {
		ReactDOM.createRoot(root).render(
			<QueryClientProvider client={queryClient}>
				<RouterProvider router={router} />
			</QueryClientProvider>,
		);
	}
}

const root = document.getElementById("app");
if (!root) {
	throw new Error("Missing #app root element");
}

// The app must still mount against the real API if the mock worker fails to
// start (e.g. the service worker script can't register).
enableMocks().then(
	() => mount(root),
	(error) => {
		console.error(
			"mock worker failed to start; continuing without mocks",
			error,
		);
		mount(root);
	},
);
