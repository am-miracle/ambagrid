import assert from "node:assert/strict";
import test from "node:test";
import type { CommandDetail, MeterOperations } from "#/api/meter-operations";
import {
	formatCaseAge,
	initialPanelState,
	panelReducer,
} from "./meter-operations-state.ts";

const unused = async () => {
	throw new Error("No requests should run in reducer tests");
};
const client: MeterOperations = {
	queue: unused,
	resolved: unused,
	templates: unused,
	detail: unused,
	reconcile: unused,
	create: unused,
};
const detail = {
	command: { command_id: "command-1", meter_id: "meter-2" },
	observed_at: null,
	received_at: null,
} as CommandDetail;

test("sign out clears credentials, previous operator data and command confirmation together", () => {
	let state = panelReducer(initialPanelState(), {
		type: "credential",
		value: "secret",
	});
	state = panelReducer(state, { type: "connected", client, templates: [] });
	state = panelReducer(state, { type: "detailLoaded", detail });
	state = panelReducer(state, {
		type: "commandField",
		field: "confirmation",
		value: "meter-2",
	});
	state = panelReducer(state, { type: "search", value: "previous-command" });
	state = panelReducer(state, { type: "mode", mode: "resolved" });
	state = panelReducer(state, { type: "failed", message: "old failure" });
	assert.deepEqual(
		panelReducer(state, { type: "signedOut" }),
		initialPanelState(),
	);
});
test("changing target or command template invalidates relay confirmation", () => {
	let state = panelReducer(initialPanelState(), {
		type: "connected",
		client,
		templates: [
			{
				command_type: "disconnect_meter",
				label: "Disconnect",
				default_reason: "Maintenance",
				requires_meter_confirmation: true,
			},
		],
	});
	state = panelReducer(state, {
		type: "commandField",
		field: "confirmation",
		value: "meter-1",
	});
	state = panelReducer(state, { type: "detailLoaded", detail });
	assert.equal(state.command.meter, "meter-2");
	assert.equal(state.command.confirmation, "");
	state = panelReducer(state, {
		type: "commandField",
		field: "confirmation",
		value: "meter-2",
	});
	state = panelReducer(state, { type: "template", value: "disconnect_meter" });
	assert.equal(state.command.reason, "Maintenance");
	assert.equal(state.command.confirmation, "");
});
test("a new authenticated session replaces all previous operator data", () => {
	let state = panelReducer(initialPanelState(), {
		type: "detailLoaded",
		detail,
	});
	state = panelReducer(state, { type: "mode", mode: "resolved" });
	state = panelReducer(state, { type: "credential", value: "secret" });
	state = panelReducer(state, { type: "connected", client, templates: [] });
	assert.equal(state.credential, "");
	assert.equal(state.detail, null);
	assert.equal(state.list.mode, "open");
	assert.equal(state.busy, true);
});

test("case age reads in hours, then days", () => {
	assert.equal(formatCaseAge(0), "0h");
	assert.equal(formatCaseAge(7200), "2h");
	assert.equal(formatCaseAge(86400 * 8), "8d");
});
