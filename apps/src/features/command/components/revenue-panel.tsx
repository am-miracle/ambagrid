import type { ReactNode } from "react";
import {
	useDevAuditEvents,
	useDevCommands,
	useDevCustomers,
} from "#/api/queries";
import type { AuditEvent, CustomerSummary, MeterCommand } from "#/api/types";
import { Badge } from "#/components/ui/badge";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "#/components/ui/tabs";
import { formatDuration } from "#/lib/format";
import { EmptyState, PanelHeader, PanelTitle } from "../ui";

type Tab = "customers" | "commands" | "audit";

const TAB_LABELS: Record<Tab, string> = {
	customers: "Customers",
	commands: "Commands",
	audit: "Audit Trail",
};

const COMMAND_STATUS_BADGE: Partial<
	Record<string, "warning" | "info" | "success" | "critical">
> = {
	requested: "warning",
	sent: "info",
	acknowledged: "success",
	failed: "critical",
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

function Rows({ count, children }: { count: number; children: ReactNode }) {
	if (count === 0) return <EmptyState>{EMPTY_MSG}</EmptyState>;
	return <div className="flex-1 overflow-y-auto min-h-0">{children}</div>;
}

function Row({ children }: { children: ReactNode }) {
	return (
		<div className="py-2.25 px-4 border-b border-[rgba(122,186,212,0.07)]">
			{children}
		</div>
	);
}

function Stat({ label, children }: { label: string; children: ReactNode }) {
	return (
		<div className="flex justify-between text-[11px]">
			<span className="text-muted-foreground">{label}</span>
			<span className="mono text-[13px]">{children}</span>
		</div>
	);
}

function CustomersTab({ data }: { data: CustomerSummary[] }) {
	return (
		<Rows count={data.length}>
			{data.map((c) => (
				<Row key={c.customer_id}>
					<div className="flex justify-between items-baseline mb-1">
						<span className="mono text-[13px]">{c.customer_id}</span>
						<span className="text-[11px] text-muted-foreground">
							{formatRelative(c.last_payment_at)}
						</span>
					</div>
					<div className="grid grid-cols-2 gap-x-3 gap-y-0.5">
						<Stat label="Balance">{c.remaining_kwh.toFixed(2)} kWh</Stat>
						<Stat label="Value">
							{formatNGN(c.remaining_money_value_minor_units)}
						</Stat>
						<Stat label="Payments">{c.total_payments}</Stat>
						<Stat label="Purchased">
							{c.total_kwh_purchased.toFixed(2)} kWh
						</Stat>
					</div>
				</Row>
			))}
		</Rows>
	);
}

function CommandsTab({ data }: { data: MeterCommand[] }) {
	return (
		<Rows count={data.length}>
			{data.map((cmd) => (
				<Row key={cmd.command_id}>
					<div className="flex justify-between items-center mb-1">
						<span className="mono text-[13px] capitalize">
							{cmd.command_type}
						</span>
						<Badge variant={COMMAND_STATUS_BADGE[cmd.status] ?? "outline"}>
							{cmd.status}
						</Badge>
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
				</Row>
			))}
		</Rows>
	);
}

function AuditTab({ data }: { data: AuditEvent[] }) {
	return (
		<Rows count={data.length}>
			{data.map((ev) => (
				<Row key={ev.event_id}>
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
				</Row>
			))}
		</Rows>
	);
}

export function RevenuePanel() {
	const { data: customers = [] } = useDevCustomers();
	const { data: commands = [] } = useDevCommands();
	const { data: auditEvents = [] } = useDevAuditEvents();

	return (
		<>
			<PanelHeader>
				<PanelTitle>Revenue</PanelTitle>
				<span className="mono muted text-[11.5px]">
					{customers.length} customers
				</span>
			</PanelHeader>
			<Tabs defaultValue="customers" className="flex-1">
				<TabsList className="px-4 py-2 border-b border-(--edge)">
					{(Object.keys(TAB_LABELS) as Tab[]).map((t) => (
						<TabsTrigger key={t} value={t}>
							{TAB_LABELS[t]}
						</TabsTrigger>
					))}
				</TabsList>
				<TabsContent value="customers">
					<CustomersTab data={customers} />
				</TabsContent>
				<TabsContent value="commands">
					<CommandsTab data={commands} />
				</TabsContent>
				<TabsContent value="audit">
					<AuditTab data={auditEvents} />
				</TabsContent>
			</Tabs>
		</>
	);
}
