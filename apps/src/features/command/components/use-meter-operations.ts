import { useReducer, useRef } from "react";
import {
	ARCHIVE_PAGE_SIZE,
	type MeterOperations,
	meterOperations,
	type ResolutionRequest,
} from "#/api/meter-operations";
import {
	initialPanelState,
	type ListMode,
	panelReducer,
} from "./meter-operations-state";

export function useMeterOperations() {
	const [state, dispatch] = useReducer(
		panelReducer,
		undefined,
		initialPanelState,
	);
	const generation = useRef(0);
	const inFlight = useRef(false);
	const pending = useRef<{ signature: string; id: string } | null>(null);
	async function run(action: (version: number) => Promise<void>) {
		if (inFlight.current) return;
		inFlight.current = true;
		const version = generation.current;
		dispatch({ type: "started" });
		try {
			await action(version);
		} catch (error) {
			if (generation.current === version)
				dispatch({
					type: "failed",
					message: error instanceof Error ? error.message : "Request failed",
				});
		} finally {
			inFlight.current = false;
			if (generation.current === version) dispatch({ type: "finished" });
		}
	}
	async function load(
		client: MeterOperations,
		version: number,
		mode = state.list.mode,
		append = false,
	) {
		if (mode === "resolved") {
			const result = await client.resolved(
				append ? state.list.nextArchive : "",
			);
			if (generation.current !== version) return;
			dispatch({
				type: "archiveLoaded",
				items: result.data,
				next:
					result.data.length === ARCHIVE_PAGE_SIZE
						? (result.data.at(-1)?.command_id ?? "")
						: "",
				append,
			});
		} else {
			const result = await client.queue(append ? state.list.nextQueue : "");
			if (generation.current !== version) return;
			dispatch({
				type: "queueLoaded",
				items: result.data,
				next: result.page.next_cursor,
				append,
			});
		}
	}
	async function detail(client: MeterOperations, id: string, version: number) {
		const result = await client.detail(id);
		if (generation.current === version)
			dispatch({ type: "detailLoaded", detail: result.data });
	}
	return {
		state,
		dispatch,
		connect: () =>
			run(async (version) => {
				const client = meterOperations(state.credential);
				const templates = await client.templates();
				if (generation.current !== version) return;
				dispatch({ type: "connected", client, templates: templates.data });
				await load(client, version, "open");
			}),
		signOut: () => {
			if (inFlight.current) return;
			generation.current++;
			pending.current = null;
			dispatch({ type: "signedOut" });
		},
		refresh: (append = false) =>
			run(async (version) => {
				if (!state.session) return;
				await load(state.session.client, version, state.list.mode, append);
				if (!append && state.detail)
					await detail(
						state.session.client,
						state.detail.command.command_id,
						version,
					);
			}),
		changeMode: (mode: ListMode) =>
			run(async (version) => {
				if (state.session) {
					dispatch({ type: "mode", mode });
					await load(state.session.client, version, mode);
				}
			}),
		select: (id: string) =>
			run(async (version) => {
				if (state.session) await detail(state.session.client, id, version);
			}),
		reconcile: (request: ResolutionRequest) =>
			run(async (version) => {
				if (!state.session || !state.detail) return;
				const client = state.session.client;
				await client.reconcile(state.detail.command.command_id, request);
				await detail(client, state.detail.command.command_id, version);
				await load(client, version);
			}),
		create: () =>
			run(async (version) => {
				if (!state.session) return;
				const { type, meter, reason, confirmation } = state.command;
				const body = {
					meter_id: meter,
					command_type: type,
					reason,
					...(type !== "request_meter_status"
						? { confirm_meter_id: confirmation }
						: {}),
				};
				const signature = JSON.stringify(body);
				if (pending.current?.signature !== signature)
					pending.current = { signature, id: crypto.randomUUID() };
				const result = await state.session.client.create({
					...body,
					request_id: pending.current.id,
				});
				await detail(state.session.client, result.data.command_id, version);
				pending.current = null;
			}),
	};
}
