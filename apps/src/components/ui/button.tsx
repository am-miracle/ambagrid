import { Button as ButtonPrimitive } from "@base-ui/react/button";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

const buttonVariants = cva(
	"group/button inline-flex shrink-0 items-center justify-center gap-1 border border-transparent whitespace-nowrap transition-colors outline-none select-none disabled:pointer-events-none disabled:opacity-35 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-3.5",
	{
		variants: {
			variant: {
				default:
					"border-primary bg-primary font-semibold text-primary-foreground hover:bg-primary/85",
				outline:
					"border-border text-foreground hover:border-primary hover:text-primary",
				muted:
					"border-border text-muted-foreground hover:border-primary hover:text-primary",
				success:
					"border-success/45 bg-success/9 text-success hover:border-primary hover:text-primary",
				command:
					"border-[#2f8ea3] bg-linear-to-b from-[#1d5f6d] to-[#113c47] font-semibold text-secondary-foreground hover:from-[#24707f] hover:to-[#14495a]",
				ghost: "text-muted-foreground hover:text-foreground",
				link: "text-muted-foreground hover:text-primary",
			},
			size: {
				default: "h-7 px-2.75 text-[12.5px]",
				sm: "h-6 px-2 text-[11px]",
				lg: "h-9 px-3.5 text-[13px]",
				icon: "size-7",
				inline: "h-auto p-0 text-[12.5px]",
			},
		},
		defaultVariants: {
			variant: "outline",
			size: "default",
		},
	},
);

function Button({
	className,
	variant,
	size,
	...props
}: ButtonPrimitive.Props & VariantProps<typeof buttonVariants>) {
	return (
		<ButtonPrimitive
			data-slot="button"
			className={cn(buttonVariants({ variant, size, className }))}
			{...props}
		/>
	);
}

export { Button, buttonVariants };
