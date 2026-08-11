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
        "data-[state=checked]:bg-primary data-[state=unchecked]:bg-secondary",
        className,
      )}
      data-slot="switch"
      {...props}
    >
      <SwitchPrimitive.Thumb
        className={cn(
          "pointer-events-none block size-4 rounded-full bg-card ring-0 transition-transform",
          "data-[state=checked]:translate-x-[18px] data-[state=unchecked]:translate-x-0.5",
        )}
      />
    </SwitchPrimitive.Root>
  );
}
