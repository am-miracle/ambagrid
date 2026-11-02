import { request } from "./client";
import type { CollectionBody, MeterCommand, ObjectBody } from "./types";

export interface ReconciliationCase {
	command_id: string;
	state: "investigating" | "resolved";
	needs_review: boolean;
	owner: string;
	finding: string;
	disposition: string;
	reason: string;
	evidence: string;
	resolved_by?: string;
	replacement_command_id?: string;
	refund_reference?: string;
	updated_at: string;
}
export interface ReconciliationItem {
	command_id: string;
	meter_id: string;
	site_id: string;
	status: string;
	reconciliation_reason: string;
	age_seconds: number;
	needs_review: boolean;
	latest_conflict?: Omit<
		RetainedCommandReport,
		"report_id" | "received_at" | "conflict_count"
	>;
	latest_conflict_received_at?: string;
	case?: ReconciliationCase;
}
export interface CommandObservation {
	relay_closed?: boolean;
	balance_kwh?: number;
	tamper_detected?: boolean;
	firmware_version?: string;
	fault_code?: string;
}
export interface RetainedCommandReport {
	report_id: string;
	conflict_count: number;
	status: string;
	occurred_at: string;
	received_at: string;
	failure_reason?: string;
	observation?: CommandObservation;
}
export interface CommandDetail {
	latest_conflict?: RetainedCommandReport;
	command: MeterCommand;
	observation?: CommandObservation;
	observed_at: string | null;
	received_at: string | null;
	case?: ReconciliationCase;
}
export interface CommandTemplate {
	command_type: string;
	label: string;
	default_reason: string;
	requires_meter_confirmation: boolean;
}
export interface ResolutionRequest {
	reviewed_conflict_id?: string;
	reviewed_conflict_count?: number;
	owner: string;
	finding: string;
	disposition: string;
	reason: string;
	evidence: string;
	refund_reference?: string;
}
export interface CreateCommandRequest {
	request_id: string;
	meter_id: string;
	command_type: string;
	reason: string;
	confirm_meter_id?: string;
}
export const ARCHIVE_PAGE_SIZE = 50;

export function isUndeliveredCredit(command: {
	command_type: string;
	status: string;
}): boolean {
	return (
		command.command_type === "credit_meter" &&
		["failed", "expired"].includes(command.status)
	);
}

// Operator credentials live only in component memory and are never sent in URLs.
export function meterOperations(key: string) {
	function call<T>(path: string, body?: unknown): Promise<T> {
		return request<T>(
			path,
			{},
			{
				method: body === undefined ? "GET" : "POST",
				headers: {
					Authorization: `Bearer ${key}`,
					...(body === undefined ? {} : { "Content-Type": "application/json" }),
				},
				body: body === undefined ? undefined : JSON.stringify(body),
			},
		);
	}
	return {
		queue: (cursor = "") =>
			call<CollectionBody<ReconciliationItem>>(
				`/v1/meter-commands/reconciliation?limit=50&cursor=${encodeURIComponent(cursor)}`,
			),
		resolved: (after = "") =>
			call<ObjectBody<ReconciliationCase[]>>(
				`/v1/meter-commands/reconciliation/resolved?limit=${ARCHIVE_PAGE_SIZE}&after_command_id=${encodeURIComponent(after)}`,
			),
		templates: () =>
			call<ObjectBody<CommandTemplate[]>>("/v1/meter-command-templates"),
		detail: (id: string) =>
			call<ObjectBody<CommandDetail>>(
				`/v1/meter-commands/${encodeURIComponent(id)}`,
			),
		reconcile: (id: string, body: ResolutionRequest) =>
			call<ObjectBody<ReconciliationCase>>(
				`/v1/meter-commands/${encodeURIComponent(id)}/reconcile`,
				body,
			),
		create: (body: CreateCommandRequest) =>
			call<ObjectBody<MeterCommand>>("/v1/meter-commands", body),
	};
}
export type MeterOperations = ReturnType<typeof meterOperations>;
