"use client";

import * as TooltipPrimitive from "@radix-ui/react-tooltip";
import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

export const TooltipProvider = TooltipPrimitive.Provider;
export const Tooltip = TooltipPrimitive.Root;
export const TooltipTrigger = TooltipPrimitive.Trigger;

export function TooltipContent({
  children,
  className,
  sideOffset = 6,
  ...props
}: ComponentProps<typeof TooltipPrimitive.Content>) {
  return (
    <TooltipPrimitive.Portal>
      <TooltipPrimitive.Content
        className={cn(
          "z-50 w-fit rounded-sm bg-primary px-2 py-1 text-primary-foreground text-xs",
          "data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95",
          "data-[state=delayed-open]:animate-in data-[state=delayed-open]:fade-in-0 data-[state=delayed-open]:zoom-in-95",
          className,
        )}
        data-slot="tooltip-content"
        sideOffset={sideOffset}
        {...props}
      >
        {children}
        <TooltipPrimitive.Arrow className="z-50 size-2 translate-y-[calc(-50%_-_1px)] rotate-45 fill-primary" />
      </TooltipPrimitive.Content>
    </TooltipPrimitive.Portal>
  );
}
