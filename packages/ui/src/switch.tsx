"use client";

import * as SwitchPrimitive from "@radix-ui/react-switch";
import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

export function Switch({ className, ...props }: ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      className={cn(
        "peer inline-flex h-5 w-9 shrink-0 items-center rounded-full border border-transparent transition-colors",
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
          "pointer-events-none block size-4 rounded-full bg-background shadow-sm ring-0 transition-transform",
          // In the dark theme the background is near black, and an off thumb
          // of that colour disappears into a grouped block's fill; it is
          // drawn light there, as the on thumb is drawn against light.
          "dark:data-[state=unchecked]:bg-foreground",
          "data-[state=checked]:translate-x-[18px] data-[state=unchecked]:translate-x-0.5",
        )}
      />
    </SwitchPrimitive.Root>
  );
}
