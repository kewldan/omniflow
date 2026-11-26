"use client";

import * as HoverCardPrimitive from "@radix-ui/react-hover-card";
import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

/**
 * A preview that opens while the pointer rests on its trigger or the trigger
 * holds keyboard focus, and closes when either leaves or on Escape. It is for
 * a glance only: everything in it must also be reachable by following the
 * trigger, since a screen reader is not told it exists.
 */
export function HoverCard({
  openDelay = 400,
  closeDelay = 150,
  ...props
}: ComponentProps<typeof HoverCardPrimitive.Root>) {
  return <HoverCardPrimitive.Root closeDelay={closeDelay} openDelay={openDelay} {...props} />;
}

export const HoverCardTrigger = HoverCardPrimitive.Trigger;

export function HoverCardContent({
  align = "start",
  className,
  sideOffset = 6,
  ...props
}: ComponentProps<typeof HoverCardPrimitive.Content>) {
  return (
    <HoverCardPrimitive.Portal>
      <HoverCardPrimitive.Content
        align={align}
        className={cn(
          "z-50 w-72 rounded-lg border border-border bg-popover p-4 text-popover-foreground",
          "shadow-[var(--glass-shadow)] outline-none",
          "data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95",
          "data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95",
          className,
        )}
        collisionPadding={8}
        data-slot="hover-card-content"
        sideOffset={sideOffset}
        {...props}
      />
    </HoverCardPrimitive.Portal>
  );
}
