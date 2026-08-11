import * as LabelPrimitive from "@radix-ui/react-label";
import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

export function Label({ className, ...props }: ComponentProps<typeof LabelPrimitive.Root>) {
  return (
    <LabelPrimitive.Root
      className={cn(
        "flex select-none items-center gap-2 font-medium text-foreground text-sm leading-none",
        "group-data-[disabled=true]:pointer-events-none group-data-[disabled=true]:opacity-50",
        className,
      )}
      data-slot="label"
      {...props}
    />
  );
}

/**
 * The design's quiet metadata style: uppercase mono, wide tracking. Used for
 * table column groups, key/value captions, and section eyebrows.
 */
export function FieldCaption({ className, ...props }: ComponentProps<"p">) {
  return (
    <p
      className={cn(
        "font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]",
        className,
      )}
      data-slot="field-caption"
      {...props}
    />
  );
}
