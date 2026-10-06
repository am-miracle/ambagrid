import { Tabs as TabsPrimitive } from "@base-ui/react/tabs";

import { cn } from "@/lib/utils";

function Tabs({ className, ...props }: TabsPrimitive.Root.Props) {
	return (
		<TabsPrimitive.Root
			data-slot="tabs"
			className={cn("flex min-h-0 flex-col", className)}
			{...props}
		/>
	);
}

function TabsList({ className, ...props }: TabsPrimitive.List.Props) {
	return (
		<TabsPrimitive.List
			data-slot="tabs-list"
			className={cn("flex gap-1", className)}
			{...props}
		/>
	);
}

function TabsTrigger({ className, ...props }: TabsPrimitive.Tab.Props) {
	return (
		<TabsPrimitive.Tab
			data-slot="tabs-trigger"
			className={cn(
				"border border-border px-2.5 py-0.75 font-mono text-[11px] text-muted-foreground whitespace-nowrap outline-none not-data-active:hover:border-foreground not-data-active:hover:text-foreground disabled:pointer-events-none disabled:opacity-35 data-active:border-primary data-active:bg-primary data-active:text-primary-foreground",
				className,
			)}
			{...props}
		/>
	);
}

function TabsContent({ className, ...props }: TabsPrimitive.Panel.Props) {
	return (
		<TabsPrimitive.Panel
			data-slot="tabs-content"
			className={cn("flex min-h-0 flex-1 flex-col outline-none", className)}
			{...props}
		/>
	);
}

export { Tabs, TabsContent, TabsList, TabsTrigger };
