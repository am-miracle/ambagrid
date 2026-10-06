import { mergeProps } from "@base-ui/react/merge-props";
import { useRender } from "@base-ui/react/use-render";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

const badgeVariants = cva(
	"inline-flex w-fit shrink-0 items-center justify-center gap-1 border px-1.5 py-px font-mono text-[10px] whitespace-nowrap",
	{
		variants: {
			variant: {
				outline: "border-(--edge) text-muted-foreground",
				critical: "border-destructive/40 text-[#ff8b96]",
				warning: "border-warning/40 text-warning",
				info: "border-info/35 text-info",
				success: "border-success/40 text-success",
				count:
					"border-transparent bg-destructive px-1.25 text-[11px] text-destructive-foreground",
			},
		},
		defaultVariants: {
			variant: "outline",
		},
	},
);

function Badge({
	className,
	variant = "outline",
	render,
	...props
}: useRender.ComponentProps<"span"> & VariantProps<typeof badgeVariants>) {
	return useRender({
		defaultTagName: "span",
		props: mergeProps<"span">(
			{
				className: cn(badgeVariants({ variant }), className),
			},
			props,
		),
		render,
		state: {
			slot: "badge",
			variant,
		},
	});
}

export { Badge, badgeVariants };
