"use client";

import * as PopoverPrimitive from "@radix-ui/react-popover";
import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

export const Popover = PopoverPrimitive.Root;
export const PopoverTrigger = PopoverPrimitive.Trigger;
export const PopoverAnchor = PopoverPrimitive.Anchor;

export function PopoverContent({
  align = "center",
  className,
  sideOffset = 6,
  ...props
}: ComponentProps<typeof PopoverPrimitive.Content>) {
  return (
    <PopoverPrimitive.Portal>
      <PopoverPrimitive.Content
        align={align}
        className={cn(
          "z-50 w-72 rounded-lg border border-border bg-popover p-4 text-popover-foreground",
          "shadow-[var(--glass-shadow)] outline-none",
          "data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95",
          "data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95",
          className,
        )}
        data-slot="popover-content"
        sideOffset={sideOffset}
        {...props}
      />
    </PopoverPrimitive.Portal>
  );
}
