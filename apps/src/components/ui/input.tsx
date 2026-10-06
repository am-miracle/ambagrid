import { Input as InputPrimitive } from "@base-ui/react/input";
import type * as React from "react";

import { cn } from "@/lib/utils";

function Input({ className, type, ...props }: React.ComponentProps<"input">) {
	return (
		<InputPrimitive
			type={type}
			data-slot="input"
			className={cn(
				"h-7 w-full min-w-0 border border-input bg-[rgba(4,8,13,0.8)] px-2 text-xs text-foreground outline-none placeholder:text-muted-foreground focus-visible:border-primary focus-visible:shadow-[0_0_0_2px_rgba(143,246,255,0.08)] focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive",
				className,
			)}
			{...props}
		/>
	);
}

export { Input };
