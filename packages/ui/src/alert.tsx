import { cva, type VariantProps } from "class-variance-authority";
import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

const alertVariants = cva(
  cn(
    "relative grid w-full grid-cols-[0_1fr] items-start gap-y-1 rounded-lg border px-4 py-3 text-sm",
    "has-[>svg]:grid-cols-[calc(var(--spacing)*4)_1fr] has-[>svg]:gap-x-3",
    "[&>svg]:size-4 [&>svg]:translate-y-0.5",
  ),
  {
    defaultVariants: { variant: "default" },
    variants: {
      variant: {
        danger: "border-destructive/25 bg-destructive/8 text-destructive [&>svg]:text-destructive",
        default: "border-border bg-card text-card-foreground",
        info: "border-info/25 bg-info/8 text-info [&>svg]:text-info",
        success: "border-success/25 bg-success/8 text-success [&>svg]:text-success",
        warning: "border-warning/25 bg-warning/8 text-warning [&>svg]:text-warning",
      },
    },
  },
);

export type AlertProps = ComponentProps<"div"> & VariantProps<typeof alertVariants>;

export function Alert({ className, variant, ...props }: AlertProps) {
  return (
    <div
      className={cn(alertVariants({ className, variant }))}
      data-slot="alert"
      role="alert"
      {...props}
    />
  );
}

export function AlertTitle({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cn("col-start-2 font-medium tracking-tight", className)}
      data-slot="alert-title"
      {...props}
    />
  );
}

export function AlertDescription({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cn("col-start-2 text-current/80 text-sm [&_p]:leading-relaxed", className)}
      data-slot="alert-description"
      {...props}
    />
  );
}

export { alertVariants };
