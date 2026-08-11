import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

/*
 * Status pills in the design are low-contrast tinted chips rather than solid
 * fills, so a dense table stays readable. Each tone pairs a 12% wash with the
 * full-strength text colour, which keeps contrast above 4.5:1 in both themes.
 */
const badgeVariants = cva(
  cn(
    "inline-flex w-fit shrink-0 items-center justify-center gap-1.5 whitespace-nowrap",
    "rounded-xs px-2 py-0.5 font-medium text-xs",
    "[&_svg]:pointer-events-none [&_svg:not([class*='size-'])]:size-3",
  ),
  {
    defaultVariants: { variant: "neutral" },
    variants: {
      variant: {
        danger: "bg-destructive/12 text-destructive",
        info: "bg-info/12 text-info",
        neutral: "bg-secondary text-secondary-foreground",
        outline: "border border-border text-foreground",
        solid: "bg-primary text-primary-foreground",
        success: "bg-success/12 text-success",
        warning: "bg-warning/12 text-warning",
      },
    },
  },
);

export type BadgeProps = ComponentProps<"span"> &
  VariantProps<typeof badgeVariants> & { asChild?: boolean };

export function Badge({ asChild = false, className, variant, ...props }: BadgeProps) {
  const Component = asChild ? Slot : "span";
  return (
    <Component className={cn(badgeVariants({ className, variant }))} data-slot="badge" {...props} />
  );
}

export { badgeVariants };
