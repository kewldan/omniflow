import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

/**
 * One choice of a set, drawn like the checkbox: a ring with the theme's
 * contrast, filled with a dot when chosen.
 *
 * It stays an `<input type="radio">` underneath, with its own look, because
 * the screens that use it are option cards — a label wrapping the control
 * and its description, styled with `has-[:checked]`. The browser's grouping
 * by `name`, its arrow-key movement, and the label's click all keep working,
 * and none of the browser's own drawing is left.
 */
export function Radio({ className, ...props }: Omit<ComponentProps<"input">, "type">) {
  return (
    <input
      className={cn(
        "peer size-4 shrink-0 cursor-pointer appearance-none rounded-full border border-muted-foreground/70 bg-card transition-colors",
        "hover:border-foreground/80",
        "checked:border-primary checked:bg-primary checked:shadow-[inset_0_0_0_3px_var(--card)]",
        "focus-visible:border-ring focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/25",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      data-slot="radio"
      type="radio"
      {...props}
    />
  );
}
