import { Toggle as TogglePrimitive } from "@base-ui/react/toggle";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

const toggleVariants = cva(
	"inline-flex items-center gap-2 border whitespace-nowrap outline-none disabled:pointer-events-none disabled:opacity-35",
	{
		variants: {
			variant: {
				default:
					"w-full border-transparent px-2 py-1.25 text-[13px] text-muted-foreground hover:border-border data-pressed:border-(--edge) data-pressed:bg-[rgba(143,246,255,0.04)] data-pressed:text-foreground",
				chip: "justify-center border-border px-2.5 py-1 text-xs text-muted-foreground not-data-pressed:hover:text-foreground data-pressed:border-primary data-pressed:bg-primary data-pressed:font-semibold data-pressed:text-primary-foreground",
			},
		},
		defaultVariants: {
			variant: "default",
		},
	},
);

function Toggle({
	className,
	variant,
	...props
}: TogglePrimitive.Props & VariantProps<typeof toggleVariants>) {
	return (
		<TogglePrimitive
			data-slot="toggle"
			className={cn(toggleVariants({ variant, className }))}
			{...props}
		/>
	);
}

export { Toggle, toggleVariants };
