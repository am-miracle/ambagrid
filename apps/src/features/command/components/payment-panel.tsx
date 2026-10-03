import { useEffect, useRef, useState } from "react";
import { useApplyDevPayment } from "#/api/queries";

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
	const onCloseRef = useRef(onClose);
	onCloseRef.current = onClose;
	const paymentRef = useRef(payment);
	paymentRef.current = payment;

	const dismiss = () => {
		paymentRef.current.reset();
		onCloseRef.current();
	};

	useEffect(() => {
		if (!open) return;
		firstInputRef.current?.focus();
		const onKey = (e: KeyboardEvent) => {
			if (e.key === "Escape") {
				paymentRef.current.reset();
				onCloseRef.current();
			}
		};
		window.addEventListener("keydown", onKey);
		return () => window.removeEventListener("keydown", onKey);
	}, [open]);

	if (!open) return null;

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
		<div
			className="fixed inset-0 z-80 grid place-items-center bg-[rgba(2,6,10,0.72)] backdrop-blur-[5px]"
			onMouseDown={(e) => {
				if (e.target === e.currentTarget) dismiss();
			}}
		>
			<section
				className="panel-anim w-[min(520px,calc(100vw-32px))] border border-border bg-[#08111a] shadow-[0_30px_90px_rgba(0,0,0,0.72)]"
				role="dialog"
				aria-modal="true"
				aria-labelledby="payment-panel-title"
			>
				<div className="flex items-center gap-2.5 border-b border-(--edge) px-4 py-3.25">
					<div className="flex-1">
						<div className="mb-0.75 font-mono text-[10px] uppercase tracking-[0.12em] text-success">
							Revenue loop
						</div>
						<h2
							id="payment-panel-title"
							className="m-0 font-heading text-base font-semibold"
						>
							Inject test payment
						</h2>
					</div>
					<button
						type="button"
						className="border border-border px-2.75 py-1.5 text-[12.5px] text-foreground hover:border-primary"
						onClick={dismiss}
					>
						Close
					</button>
				</div>

				<form
					className="grid grid-cols-[1fr_0.8fr] gap-3 px-4 py-4.5"
					onSubmit={submit}
				>
					<label className="grid gap-1.5 text-[11px] text-muted-foreground">
						<span>Customer ID</span>
						<input
							ref={firstInputRef}
							className="w-full border border-input bg-[rgba(255,255,255,0.025)] px-2.5 py-2.25 font-mono text-[13px] text-foreground outline-none focus:border-primary focus:shadow-[0_0_0_2px_rgba(143,246,255,0.08)]"
							value={customerId}
							onChange={(event) => setCustomerId(event.target.value)}
							required
						/>
					</label>
					<label className="grid gap-1.5 text-[11px] text-muted-foreground">
						<span>Amount (major)</span>
						<div className="flex items-center border border-input">
							<b className="pl-2.5 font-mono text-[10px] text-primary">NGN</b>
							<input
								className="w-full border-0 bg-[rgba(255,255,255,0.025)] px-2.5 py-2.25 font-mono text-[13px] text-foreground outline-none focus:shadow-[0_0_0_2px_rgba(143,246,255,0.08)]"
								type="number"
								min="0.01"
								step="0.01"
								value={amount}
								onChange={(event) => setAmount(event.target.value)}
								required
							/>
						</div>
					</label>
					<button
						type="submit"
						className="col-span-full border border-success/50 bg-success/9 px-3.5 py-2.5 text-[13px] text-success disabled:opacity-[0.55]"
						disabled={payment.isPending}
					>
						{payment.isPending ? "Applying payment…" : "Apply payment"}
					</button>
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
							<div className="grid gap-1 border-l-2 border-success bg-black/18 p-2.5">
								<span className="text-[10px] text-muted-foreground">
									Credit issued
								</span>
								<b className="font-mono text-lg font-medium">
									{result.credit.kwh_granted.toFixed(2)} kWh
								</b>
							</div>
							<div className="grid gap-1 border-l-2 border-success bg-black/18 p-2.5">
								<span className="text-[10px] text-muted-foreground">
									Current balance
								</span>
								<b className="font-mono text-lg font-medium">
									{result.balance.remaining_kwh.toFixed(2)} kWh
								</b>
							</div>
						</div>
						<div className="mt-1 text-[11px] text-muted-foreground">
							Credit issued
							{result.meter_command
								? ` · meter reconnect ${result.meter_command.status}`
								: " · no reconnect needed"}
						</div>
					</div>
				)}
			</section>
		</div>
	);
}
