"use client";

import * as TabsPrimitive from "@radix-ui/react-tabs";
import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

export const Tabs = TabsPrimitive.Root;

export function TabsList({ className, ...props }: ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      className={cn(
        "inline-flex h-9 w-fit items-center justify-center gap-1 rounded-md bg-secondary p-1",
        className,
      )}
      data-slot="tabs-list"
      {...props}
    />
  );
}

export function TabsTrigger({ className, ...props }: ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      className={cn(
        "inline-flex flex-1 items-center justify-center gap-1.5 whitespace-nowrap rounded-xs px-2.5 py-1",
        "font-medium text-[13px] text-muted-foreground transition-colors",
        "focus-visible:ring-[3px] focus-visible:ring-ring/40 focus-visible:outline-none",
        "disabled:pointer-events-none disabled:opacity-50",
        "data-[state=active]:bg-card data-[state=active]:text-foreground",
        "[&_svg]:pointer-events-none [&_svg:not([class*='size-'])]:size-4",
        className,
      )}
      data-slot="tabs-trigger"
      {...props}
    />
  );
}

export function TabsContent({ className, ...props }: ComponentProps<typeof TabsPrimitive.Content>) {
  return (
    <TabsPrimitive.Content
      className={cn("flex-1 outline-none", className)}
      data-slot="tabs-content"
      {...props}
    />
  );
}
