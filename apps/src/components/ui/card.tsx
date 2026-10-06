import type * as React from "react";

import { cn } from "@/lib/utils";

function Card({ className, ...props }: React.ComponentProps<"section">) {
	return (
		<section
			data-slot="card"
			className={cn(
				"border border-(--edge) bg-(--deck) px-4 py-3.5",
				className,
			)}
			{...props}
		/>
	);
}

function CardTitle({ className, ...props }: React.ComponentProps<"h2">) {
	return (
		<h2
			data-slot="card-title"
			className={cn("m-0 mb-2 text-base font-semibold", className)}
			{...props}
		/>
	);
}

export { Card, CardTitle };
