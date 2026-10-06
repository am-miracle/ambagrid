import type * as React from "react";

import { cn } from "@/lib/utils";

function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
	return (
		<textarea
			data-slot="textarea"
			className={cn(
				"w-full min-w-0 resize-y border border-input bg-[rgba(4,8,13,0.8)] px-2 py-1.25 font-sans text-[13px] text-foreground outline-none placeholder:text-muted-foreground focus-visible:border-primary focus-visible:shadow-[0_0_0_2px_rgba(143,246,255,0.08)] focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive",
				className,
			)}
			{...props}
		/>
	);
}

export { Textarea };
