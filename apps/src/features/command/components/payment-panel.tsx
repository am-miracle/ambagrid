import { useRef, useState } from "react";
import { useApplyDevPayment } from "#/api/queries";
import { Button } from "#/components/ui/button";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogHeader,
	DialogTitle,
} from "#/components/ui/dialog";
import { Input } from "#/components/ui/input";
import { Label } from "#/components/ui/label";

export function PaymentPanel({
	open,
	onClose,
}: {
	open: boolean;
	onClose: () => void;
}) {
	const payment = useApplyDevPayment();
	const [customerId, setCustomerId] = useState("cust-01");
	const [amount, setAmount] = useState("5000");
	const firstInputRef = useRef<HTMLInputElement>(null);

	const result = payment.data?.data;
	const submit = (event: React.FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		const majorUnits = Number(amount);
		if (!Number.isFinite(majorUnits) || majorUnits <= 0) return;
		payment.mutate({
			customerId,
			amountMinorUnits: Math.round(majorUnits * 100),
			currency: "NGN",
		});
	};

	return (
		<Dialog
			open={open}
			onOpenChange={(next) => {
				if (next) return;
				payment.reset();
				onClose();
			}}
		>
			<DialogContent initialFocus={firstInputRef}>
				<DialogHeader>
					<div className="flex-1">
						<div className="mb-0.75 font-mono text-[10px] uppercase tracking-[0.12em] text-success">
							Revenue loop
						</div>
						<DialogTitle>Inject test payment</DialogTitle>
					</div>
					<DialogClose render={<Button />}>Close</DialogClose>
				</DialogHeader>

				<form
					className="grid grid-cols-[1fr_0.8fr] gap-3 px-4 py-4.5"
					onSubmit={submit}
				>
					<div className="grid gap-1.5">
						<Label htmlFor="payment-customer">Customer ID</Label>
						<Input
							ref={firstInputRef}
							id="payment-customer"
							className="h-9 px-2.5 font-mono text-[13px]"
							value={customerId}
							onChange={(event) => setCustomerId(event.target.value)}
							required
						/>
					</div>
					<div className="grid gap-1.5">
						<Label htmlFor="payment-amount">Amount (major)</Label>
						<div className="flex items-center border border-input bg-[rgba(4,8,13,0.8)] focus-within:border-primary">
							<b className="pl-2.5 font-mono text-[10px] text-primary">NGN</b>
							<Input
								id="payment-amount"
								className="h-9 border-0 bg-transparent px-2.5 font-mono text-[13px] focus-visible:shadow-none"
								type="number"
								min="0.01"
								step="0.01"
								value={amount}
								onChange={(event) => setAmount(event.target.value)}
								required
							/>
						</div>
					</div>
					<Button
						type="submit"
						variant="success"
						size="lg"
						className="col-span-full"
						disabled={payment.isPending}
					>
						{payment.isPending ? "Applying payment…" : "Apply payment"}
					</Button>
				</form>

				{payment.error && (
					<div className="mx-4 mb-4 border border-destructive/45 bg-destructive/8 px-2.75 py-2.25 text-xs text-[#ff9ba5]">
						{payment.error.message}
					</div>
				)}

				{result && (
					<div
						className="mx-4 mb-4.5 border border-success/35 bg-success/5 p-3.5"
						aria-live="polite"
					>
						<div className="font-mono text-[11px] uppercase tracking-widest text-success">
							Payment confirmed
						</div>
						<div className="my-3 grid grid-cols-2 gap-2.5">
							<ResultTile
								label="Credit issued"
								kwh={result.credit.kwh_granted}
							/>
							<ResultTile
								label="Current balance"
								kwh={result.balance.remaining_kwh}
							/>
						</div>
						<div className="mt-1 text-[11px] text-muted-foreground">
							Credit issued
							{result.meter_command
								? ` · meter reconnect ${result.meter_command.status}`
								: " · no reconnect needed"}
						</div>
					</div>
				)}
			</DialogContent>
		</Dialog>
	);
}

function ResultTile({ label, kwh }: { label: string; kwh: number }) {
	return (
		<div className="grid gap-1 border-l-2 border-success bg-black/18 p-2.5">
			<span className="text-[10px] text-muted-foreground">{label}</span>
			<b className="font-mono text-lg font-medium">{kwh.toFixed(2)} kWh</b>
		</div>
	);
}
