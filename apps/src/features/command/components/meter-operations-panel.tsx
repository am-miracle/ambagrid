import { useId, useReducer } from "react";
import type {
	CommandDetail,
	CommandObservation,
	ResolutionRequest,
} from "#/api/meter-operations";
import { isUndeliveredCredit } from "#/api/meter-operations";
import { Button } from "#/components/ui/button";
import { Input } from "#/components/ui/input";
import { PanelHeader, PanelTitle } from "../ui";
import { formatCaseAge } from "./meter-operations-state";
import { useMeterOperations } from "./use-meter-operations";

const field = "w-full border border-input bg-background p-2 text-xs";
function timestamp(value?: string | null) {
	return value ? new Date(value).toLocaleString() : "Unknown";
}
function Observation({
	value,
	observedAt,
	receivedAt,
}: {
	value?: CommandObservation;
	observedAt?: string | null;
	receivedAt?: string | null;
}) {
	return (
		<div className="text-xs space-y-1">
			<p>Observed: {timestamp(observedAt)}</p>
			<p>Received: {timestamp(receivedAt)}</p>
			{observedAt && (
				<p>
					{Date.now() - Date.parse(observedAt) > 300000
						? "Observation is stale (over 5 minutes old)"
						: "Observation is within the last 5 minutes"}
				</p>
			)}
			{value ? (
				<>
					<p>
						Relay:{" "}
						{value.relay_closed === undefined
							? "Unknown"
							: value.relay_closed
								? "Closed"
								: "Open"}
					</p>
					<p>Balance: {value.balance_kwh ?? "Unknown"} kWh</p>
					<p>
						Tamper:{" "}
						{value.tamper_detected === undefined
							? "Unknown"
							: value.tamper_detected
								? "Detected"
								: "Clear"}
					</p>
					<p>Firmware: {value.firmware_version || "Unknown"}</p>
					<p>Fault: {value.fault_code || "Not reported"}</p>
				</>
			) : (
				<p className="text-muted-foreground">No device observation reported.</p>
			)}
		</div>
	);
}
interface DecisionFields {
	disposition: string;
	finding: string;
	owner: string;
	reason: string;
	evidence: string;
	refund: string;
}
function decisionReducer(
	state: DecisionFields,
	change: Partial<DecisionFields>,
): DecisionFields {
	return { ...state, ...change };
}
function DecisionForm({
	detail,
	busy,
	onSave,
}: {
	detail: CommandDetail;
	busy: boolean;
	onSave: (request: ResolutionRequest) => Promise<void>;
}) {
	const id = useId();
	const resolved = detail.case?.state === "resolved";
	const [form, edit] = useReducer(decisionReducer, {
		disposition: resolved ? "review" : "investigate",
		finding: "unknown",
		owner: detail.case?.owner ?? "",
		reason: "",
		evidence: "",
		refund: "",
	});
	const decision = detail.case;
	const summary =
		decision?.state === "resolved" ? (
			<p className="text-xs">
				Resolved as {decision.disposition} by {decision.resolved_by}.{" "}
				{decision.evidence}
				{decision.replacement_command_id && (
					<span className="block mono break-all">
						Replacement: {decision.replacement_command_id}
					</span>
				)}
				{decision.refund_reference && (
					<span className="block">Refund: {decision.refund_reference}</span>
				)}
			</p>
		) : null;
	if (resolved && !decision?.needs_review) return summary;
	return (
		<form
			className="grid gap-2"
			onSubmit={(e) => {
				e.preventDefault();
				void onSave({
					owner: form.owner,
					finding: form.finding,
					disposition: form.disposition,
					reason: form.reason,
					evidence: form.evidence,
					...(form.disposition === "refund"
						? { refund_reference: form.refund }
						: {}),
					...(resolved
						? {
								reviewed_conflict_count: detail.latest_conflict?.conflict_count,
								reviewed_conflict_id: detail.latest_conflict?.report_id,
							}
						: {}),
				});
			}}
		>
			{summary}
			{resolved ? (
				<p className="text-xs">
					Review the conflicting report above. This records your assessment and
					preserves the original decision.
				</p>
			) : (
				<>
					<label className="text-xs">
						Finding
						<select
							className={field}
							value={form.finding}
							onChange={(e) => edit({ finding: e.target.value })}
						>
							<option value="unknown">Delivery uncertain</option>
							<option value="delivered">Confirmed delivered</option>
							<option value="not_delivered">Confirmed undelivered</option>
						</select>
					</label>
					<label className="text-xs">
						Action
						<select
							className={field}
							value={form.disposition}
							onChange={(e) => edit({ disposition: e.target.value })}
						>
							<option value="investigate">Assign investigation</option>
							<option value="delivered">Resolve as delivered</option>
							<option value="redeliver">Redeliver credit</option>
							<option value="refund">Record completed refund</option>
						</select>
					</label>
					<label className="text-xs" htmlFor={`${id}-owner`}>
						Owner
						<Input
							id={`${id}-owner`}
							value={form.owner}
							onChange={(e) => edit({ owner: e.target.value })}
							required={form.disposition === "investigate"}
							maxLength={200}
						/>
					</label>
				</>
			)}
			<label className="text-xs" htmlFor={`${id}-reason`}>
				Reason
				<Input
					id={`${id}-reason`}
					value={form.reason}
					onChange={(e) => edit({ reason: e.target.value })}
					required
					maxLength={1000}
				/>
			</label>
			<label className="text-xs">
				Supporting evidence
				<textarea
					className={field}
					value={form.evidence}
					onChange={(e) => edit({ evidence: e.target.value })}
					required
					maxLength={8000}
					rows={3}
				/>
			</label>
			{form.disposition === "refund" && (
				<label className="text-xs" htmlFor={`${id}-refund`}>
					Completed refund reference
					<Input
						id={`${id}-refund`}
						value={form.refund}
						onChange={(e) => edit({ refund: e.target.value })}
						required
						maxLength={200}
					/>
					<span className="text-muted-foreground">
						Records an external refund and reverses the credit entitlement.
					</span>
				</label>
			)}
			{resolved && !detail.latest_conflict && (
				<p role="alert" className="text-xs text-destructive">
					Reload this case to see its conflicting report before reviewing.
				</p>
			)}
			<Button
				type="submit"
				disabled={busy || (resolved && !detail.latest_conflict)}
			>
				{busy
					? "Saving…"
					: resolved
						? "Record evidence review"
						: "Save decision"}
			</Button>
		</form>
	);
}

export function MeterOperationsPanel() {
	const id = useId();
	const operations = useMeterOperations();
	const { state, dispatch } = operations;
	const { credential, session, list, detail, search, command, busy, error } =
		state;
	const archived = list.mode === "resolved";
	const template = session?.templates.find(
		(t) => t.command_type === command.type,
	);
	return (
		<>
			<PanelHeader>
				<PanelTitle>Meter operations</PanelTitle>
			</PanelHeader>
			<div className="flex-1 overflow-y-auto p-4 space-y-4">
				{!session ? (
					<form
						onSubmit={(e) => {
							e.preventDefault();
							void operations.connect();
						}}
						className="grid gap-2"
					>
						<label className="text-xs" htmlFor={`${id}-key`}>
							Operator key
							<Input
								id={`${id}-key`}
								type="password"
								autoComplete="off"
								value={credential}
								onChange={(e) =>
									dispatch({ type: "credential", value: e.target.value })
								}
								required
							/>
						</label>
						<Button type="submit" disabled={busy}>
							Connect
						</Button>
					</form>
				) : (
					<>
						<div className="flex gap-2">
							<Button
								disabled={busy}
								onClick={() =>
									void operations.changeMode(archived ? "open" : "resolved")
								}
							>
								{archived ? "Open queue" : "Resolved cases"}
							</Button>
							<Button disabled={busy} onClick={() => void operations.refresh()}>
								Refresh
							</Button>
							<Button disabled={busy} onClick={operations.signOut}>
								Sign out
							</Button>
						</div>
						<div className="space-y-2">
							{(archived ? list.archive : list.queue).map((item) => (
								<button
									type="button"
									key={item.command_id}
									className="block w-full border border-input p-2 text-left text-xs hover:border-primary"
									disabled={busy}
									onClick={() => void operations.select(item.command_id)}
								>
									<span className="block mono break-all">
										{item.command_id}
									</span>
									{"reconciliation_reason" in item ? (
										<>
											{item.meter_id} · {item.reconciliation_reason} ·{" "}
											{item.case?.state ?? "open"} ·{" "}
											{formatCaseAge(item.age_seconds)}
											{item.needs_review && (
												<span className="block text-destructive">
													New conflicting evidence requires review
												</span>
											)}
										</>
									) : (
										<>
											{item.disposition} · {item.owner}
										</>
									)}
								</button>
							))}
							{(archived ? list.archive : list.queue).length === 0 && (
								<p className="text-xs text-muted-foreground">
									No {archived ? "resolved" : "open"} cases.
								</p>
							)}
							{(archived ? list.nextArchive : list.nextQueue) && (
								<Button
									disabled={busy}
									onClick={() => void operations.refresh(true)}
								>
									Load more
								</Button>
							)}
						</div>
						<form
							className="flex gap-2"
							onSubmit={(e) => {
								e.preventDefault();
								void operations.select(search);
							}}
						>
							<Input
								aria-label="Command ID"
								placeholder="Find command by ID"
								value={search}
								onChange={(e) =>
									dispatch({ type: "search", value: e.target.value })
								}
								required
							/>
							<Button type="submit" disabled={busy}>
								Find
							</Button>
						</form>
						{detail && (
							<section className="space-y-3 border border-input p-3">
								<h3 className="font-heading">
									{detail.command.command_type} · {detail.command.status}
								</h3>
								<p className="mono text-xs break-all">
									{detail.command.command_id}
								</p>
								{detail.command.failure_reason && (
									<p className="text-xs text-destructive">
										{detail.command.failure_reason}
									</p>
								)}
								<h4 className="font-heading">Original device observation</h4>
								<Observation
									value={detail.observation}
									observedAt={detail.observed_at}
									receivedAt={detail.received_at}
								/>
								{detail.latest_conflict && (
									<section
										className="border border-destructive/50 p-2 space-y-1"
										aria-label="Conflicting device evidence"
									>
										<h4 className="font-heading">
											Latest conflicting device report
										</h4>
										<p className="text-xs">
											Retained reports: {detail.latest_conflict.conflict_count}.
											Reported outcome: {detail.latest_conflict.status}
										</p>
										<p className="mono text-xs break-all">
											Report: {detail.latest_conflict.report_id}
										</p>
										{detail.latest_conflict.failure_reason && (
											<p className="text-xs">
												{detail.latest_conflict.failure_reason}
											</p>
										)}
										<Observation
											value={detail.latest_conflict.observation}
											observedAt={detail.latest_conflict.occurred_at}
											receivedAt={detail.latest_conflict.received_at}
										/>
									</section>
								)}
								{(detail.case ||
									list.queue.some(
										(item) => item.command_id === detail.command.command_id,
									) ||
									isUndeliveredCredit(detail.command)) && (
									<DecisionForm
										key={`${detail.command.command_id}:${detail.case?.updated_at ?? ""}:${detail.latest_conflict?.report_id ?? ""}:${detail.latest_conflict?.conflict_count ?? 0}`}
										detail={detail}
										busy={busy}
										onSave={operations.reconcile}
									/>
								)}
							</section>
						)}
						<form
							onSubmit={(e) => {
								e.preventDefault();
								void operations.create();
							}}
							className="grid gap-2 border-t border-input pt-3"
						>
							<h3 className="font-heading">Create meter command</h3>
							<label className="text-xs">
								Template
								<select
									className={field}
									value={command.type}
									onChange={(e) =>
										dispatch({ type: "template", value: e.target.value })
									}
								>
									{session.templates.map((t) => (
										<option key={t.command_type} value={t.command_type}>
											{t.label}
										</option>
									))}
								</select>
							</label>
							<label className="text-xs" htmlFor={`${id}-meter`}>
								Meter ID
								<Input
									id={`${id}-meter`}
									value={command.meter}
									onChange={(e) =>
										dispatch({
											type: "commandField",
											field: "meter",
											value: e.target.value,
										})
									}
									required
								/>
							</label>
							<label className="text-xs" htmlFor={`${id}-reason`}>
								Reason
								<Input
									id={`${id}-reason`}
									value={command.reason}
									onChange={(e) =>
										dispatch({
											type: "commandField",
											field: "reason",
											value: e.target.value,
										})
									}
									required
									maxLength={1000}
								/>
							</label>
							{template?.requires_meter_confirmation && (
								<label className="text-xs" htmlFor={`${id}-confirmation`}>
									Confirm target meter ID
									<Input
										id={`${id}-confirmation`}
										value={command.confirmation}
										onChange={(e) =>
											dispatch({
												type: "commandField",
												field: "confirmation",
												value: e.target.value,
											})
										}
										required
									/>
								</label>
							)}
							<Button
								type="submit"
								disabled={
									busy ||
									(template?.requires_meter_confirmation &&
										command.confirmation !== command.meter)
								}
							>
								{busy ? "Working…" : (template?.label ?? "Send command")}
							</Button>
						</form>
					</>
				)}
				{error && (
					<p role="alert" className="text-xs text-destructive">
						{error}
					</p>
				)}
			</div>
		</>
	);
}
