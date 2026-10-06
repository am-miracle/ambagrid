import type * as React from "react";

import { cn } from "@/lib/utils";

function Label({ className, ...props }: React.ComponentProps<"label">) {
	return (
		// biome-ignore lint/a11y/noLabelWithoutControl: callers associate the control via htmlFor
		<label
			data-slot="label"
			className={cn(
				"font-heading text-[11.5px] tracking-[0.6px] text-muted-foreground select-none peer-disabled:opacity-50",
				className,
			)}
			{...props}
		/>
	);
}

export { Label };
