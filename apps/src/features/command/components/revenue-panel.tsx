import { useState } from "react";
import {
	useDevAuditEvents,
	useDevCommands,
	useDevCustomers,
} from "#/api/queries";
import type { AuditEvent, CustomerSummary, MeterCommand } from "#/api/types";
import { formatDuration } from "#/lib/format";

type Tab = "customers" | "commands" | "audit";

const TAB_LABELS: Record<Tab, string> = {
	customers: "Customers",
	commands: "Commands",
	audit: "Audit Trail",
};

const COMMAND_STATUS_STYLE: Record<
	string,
	{ color: string; borderColor: string }
> = {
	requested: {
		color: "var(--warning)",
		borderColor: "rgba(245,165,36,0.4)",
	},
	sent: { color: "var(--cyan)", borderColor: "rgba(143,246,255,0.35)" },
	acknowledged: {
		color: "var(--healthy)",
		borderColor: "rgba(57,215,192,0.4)",
	},
	failed: { color: "#ff8b96", borderColor: "rgba(255,77,94,0.4)" },
};

const EVENT_LABELS: Record<string, string> = {
	payment_confirmed: "Payment confirmed",
	credit_issued: "Credit issued",
	balance_updated: "Balance updated",
	meter_command_issued: "Command issued",
	meter_command_acknowledged: "Command acknowledged",
	"payment.applied": "Payment applied",
	"credit.issued": "Credit issued",
	"meter.command.requested": "Command requested",
};

function formatNGN(minorUnits: number): string {
	return `₦${(minorUnits / 100).toLocaleString("en", { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}

function formatRelative(iso: string | null): string {
	if (!iso) return "—";
	const ms = Date.now() - Date.parse(iso);
	if (ms < 0) return "just now";
	return `${formatDuration(ms)} ago`;
}

function eventDetail(event: AuditEvent): string {
	const d = event.detail;
	const parts: string[] = [];
	const amount = (d.amount_minor_units ?? d.amount) as number | undefined;
	if (typeof amount === "number") parts.push(formatNGN(amount));
	if (typeof d.kwh_granted === "number")
		parts.push(`${(d.kwh_granted as number).toFixed(2)} kWh`);
	if (typeof d.remaining_kwh === "number")
		parts.push(`bal ${(d.remaining_kwh as number).toFixed(2)} kWh`);
	if (typeof d.command_type === "string") parts.push(d.command_type as string);
	if (typeof d.status === "string") parts.push(d.status as string);
	return parts.join(" · ") || "—";
}

const EMPTY_MSG =
	"No payments recorded yet. Use the Test payment button to start.";

function CustomersTab({ data }: { data: CustomerSummary[] }) {
	if (data.length === 0)
		return (
			<div className="py-10 px-6 text-center text-muted-foreground text-[13px] leading-[1.6]">
				{EMPTY_MSG}
			</div>
		);
	return (
		<div className="flex-1 overflow-y-auto min-h-0">
			{data.map((c) => (
				<div
					key={c.customer_id}
					className="py-2.25 px-4 border-b border-[rgba(122,186,212,0.07)]"
				>
					<div className="flex justify-between items-baseline mb-1">
						<span className="mono text-[13px]">{c.customer_id}</span>
						<span className="text-[11px] text-muted-foreground">
							{formatRelative(c.last_payment_at)}
						</span>
					</div>
					<div className="grid grid-cols-2 gap-x-3 gap-y-0.5">
						<div className="flex justify-between text-[11px]">
							<span className="text-muted-foreground">Balance</span>
							<span className="mono text-[13px]">
								{c.remaining_kwh.toFixed(2)} kWh
							</span>
						</div>
						<div className="flex justify-between text-[11px]">
							<span className="text-muted-foreground">Value</span>
							<span className="mono text-[13px]">
								{formatNGN(c.remaining_money_value_minor_units)}
							</span>
						</div>
						<div className="flex justify-between text-[11px]">
							<span className="text-muted-foreground">Payments</span>
							<span className="mono text-[13px]">{c.total_payments}</span>
						</div>
						<div className="flex justify-between text-[11px]">
							<span className="text-muted-foreground">Purchased</span>
							<span className="mono text-[13px]">
								{c.total_kwh_purchased.toFixed(2)} kWh
							</span>
						</div>
					</div>
				</div>
			))}
		</div>
	);
}

function CommandsTab({ data }: { data: MeterCommand[] }) {
	if (data.length === 0)
		return (
			<div className="py-10 px-6 text-center text-muted-foreground text-[13px] leading-[1.6]">
				{EMPTY_MSG}
			</div>
		);
	return (
		<div className="flex-1 overflow-y-auto min-h-0">
			{data.map((cmd) => (
				<div
					key={cmd.command_id}
					className="py-2.25 px-4 border-b border-[rgba(122,186,212,0.07)]"
				>
					<div className="flex justify-between items-center mb-1">
						<span className="mono text-[13px] capitalize">
							{cmd.command_type}
						</span>
						<span
							className="font-mono text-[10px] py-px px-1.5 border text-muted-foreground"
							style={
								COMMAND_STATUS_STYLE[cmd.status] ?? {
									color: "var(--muted)",
									borderColor: "var(--edge)",
								}
							}
						>
							{cmd.status}
						</span>
					</div>
					<div className="flex justify-between text-[11px] text-muted-foreground">
						<span className="mono">{cmd.meter_id}</span>
						<span>{formatRelative(cmd.requested_at)}</span>
					</div>
					{cmd.reason && (
						<div className="text-[11px] text-[#b9cbd9] mt-0.5">
							{cmd.reason}
						</div>
					)}
					{cmd.failure_reason && (
						<div className="text-[11px] text-destructive mt-0.5">
							{cmd.failure_reason}
						</div>
					)}
				</div>
			))}
		</div>
	);
}

function AuditTab({ data }: { data: AuditEvent[] }) {
	if (data.length === 0)
		return (
			<div className="py-10 px-6 text-center text-muted-foreground text-[13px] leading-[1.6]">
				{EMPTY_MSG}
			</div>
		);
	return (
		<div className="flex-1 overflow-y-auto min-h-0">
			{data.map((ev) => (
				<div
					key={ev.event_id}
					className="py-2.25 px-4 border-b border-[rgba(122,186,212,0.07)]"
				>
					<div className="flex justify-between items-baseline mb-0.5">
						<span className="text-[13px]">
							{EVENT_LABELS[ev.event_type] ?? ev.event_type}
						</span>
						<span className="text-[11px] text-muted-foreground">
							{formatRelative(ev.created_at)}
						</span>
					</div>
					<div className="flex justify-between text-[11px] text-muted-foreground">
						<span className="mono">{ev.customer_id}</span>
						<span className="mono">{eventDetail(ev)}</span>
					</div>
				</div>
			))}
		</div>
	);
}

export function RevenuePanel() {
	const [tab, setTab] = useState<Tab>("customers");
	const { data: customers = [] } = useDevCustomers();
	const { data: commands = [] } = useDevCommands();
	const { data: auditEvents = [] } = useDevAuditEvents();

	return (
		<>
			<div className="flex items-center gap-2.5 py-3.25 px-4 border-b border-(--edge)">
				<h2 className="font-heading text-base font-semibold m-0 flex-1">
					Revenue
				</h2>
				<span className="mono muted text-[11.5px]">
					{customers.length} customers
				</span>
			</div>
			<div className="flex gap-1 px-4 py-2 border-b border-(--edge)">
				{(Object.keys(TAB_LABELS) as Tab[]).map((t) => (
					<button
						key={t}
						type="button"
						className={`mono text-[11px] py-0.75 px-2.5 border ${
							tab === t
								? "text-primary-foreground bg-primary border-primary"
								: "text-muted-foreground border-border hover:text-foreground hover:border-foreground"
						}`}
						onClick={() => setTab(t)}
					>
						{TAB_LABELS[t]}
					</button>
				))}
			</div>
			{tab === "customers" && <CustomersTab data={customers} />}
			{tab === "commands" && <CommandsTab data={commands} />}
			{tab === "audit" && <AuditTab data={auditEvents} />}
		</>
	);
}
