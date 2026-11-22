"use client";

import * as SwitchPrimitive from "@radix-ui/react-switch";
import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

/**
 * An on/off switch.
 *
 * 24×44 pixels, fixed rather than on the spacing scale: WCAG 2.2's target-size
 * rule wants a control at least 24 pixels across, and a 20-pixel switch only
 * passed while nothing sat near it. On a phone the floating tab bar can — the
 * customer panel's privacy page failed that check once its header was gone and
 * a switch scrolled under the bar — and a compact density shrank it further.
 */
export function Switch({ className, ...props }: ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      className={cn(
        "peer inline-flex h-[24px] w-[44px] shrink-0 items-center rounded-full border border-transparent transition-colors",
        "focus-visible:ring-[3px] focus-visible:ring-ring/40 focus-visible:outline-none",
        "disabled:cursor-not-allowed disabled:opacity-50",
        // The off track is a mid grey rather than the surface tint: on a light
        // card the tint all but vanished, leaving a white pill on white.
        "data-[state=checked]:bg-primary data-[state=unchecked]:bg-muted-foreground/35",
        "dark:data-[state=unchecked]:bg-muted-foreground/40",
        className,
      )}
      data-slot="switch"
      {...props}
    >
      <SwitchPrimitive.Thumb
        className={cn(
          "pointer-events-none block size-[20px] rounded-full bg-background shadow-sm ring-0 transition-transform",
          // In the dark theme the background is near black, and an off thumb
          // of that colour disappears into a grouped block's fill; it is
          // drawn light there, as the on thumb is drawn against light.
          "dark:data-[state=unchecked]:bg-foreground",
          "data-[state=checked]:translate-x-[21px] data-[state=unchecked]:translate-x-[1px]",
        )}
      />
    </SwitchPrimitive.Root>
  );
}
