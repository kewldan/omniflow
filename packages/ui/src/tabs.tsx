"use client";

import * as TabsPrimitive from "@radix-ui/react-tabs";
import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

export function Tabs({ className, ...props }: ComponentProps<typeof TabsPrimitive.Root>) {
  return (
    <TabsPrimitive.Root
      // The gap separates the tab row from the panel below it, so no screen
      // has to remember to add one and a bordered panel never touches the row.
      className={cn("flex flex-col gap-4", className)}
      data-slot="tabs"
      {...props}
    />
  );
}

export function TabsList({ className, ...props }: ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      className={cn(
        // `max-w-full` and horizontal scrolling keep a long tab row inside a
        // phone screen instead of widening the page; `justify-start` keeps the
        // first tab reachable when the row scrolls (centred content overflows
        // to the left, where no scrollbar can reach it).
        "inline-flex h-9 w-fit max-w-full items-center justify-start gap-1 overflow-x-auto rounded-md bg-secondary p-1 [scrollbar-width:none]",
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
