"use client";

import * as ScrollAreaPrimitive from "@radix-ui/react-scroll-area";
import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

export function ScrollArea({
  children,
  className,
  ...props
}: ComponentProps<typeof ScrollAreaPrimitive.Root>) {
  return (
    <ScrollAreaPrimitive.Root
      className={cn("relative overflow-hidden", className)}
      data-slot="scroll-area"
      {...props}
    >
      <ScrollAreaPrimitive.Viewport className="size-full rounded-[inherit] outline-none focus-visible:ring-[3px] focus-visible:ring-ring/40">
        {children}
      </ScrollAreaPrimitive.Viewport>
      <ScrollBar />
      <ScrollAreaPrimitive.Corner />
    </ScrollAreaPrimitive.Root>
  );
}

export function ScrollBar({
  className,
  orientation = "vertical",
  ...props
}: ComponentProps<typeof ScrollAreaPrimitive.ScrollAreaScrollbar>) {
  return (
    <ScrollAreaPrimitive.ScrollAreaScrollbar
      className={cn(
        "flex touch-none select-none transition-colors",
        orientation === "horizontal" ? "h-2 flex-col border-t p-px" : "w-2 border-l p-px",
        "border-transparent",
        className,
      )}
      data-slot="scroll-area-scrollbar"
      orientation={orientation}
      {...props}
    >
      <ScrollAreaPrimitive.ScrollAreaThumb className="relative flex-1 rounded-full bg-border" />
    </ScrollAreaPrimitive.ScrollAreaScrollbar>
  );
}
