import type { HTMLAttributes } from "react";

import { cn } from "./lib/utils";

/*
 * Cards and list rows share one surface treatment in the design: the raised
 * `card` tone, a hairline border, and 12px corners. Shadows are reserved for
 * floating layers, so a resting card carries none.
 */
export function Card({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("rounded-xl border border-border bg-card text-card-foreground", className)}
      data-slot="card"
      {...props}
    />
  );
}

export function CardHeader({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("flex flex-col gap-1.5 p-5", className)}
      data-slot="card-header"
      {...props}
    />
  );
}

export function CardTitle({ className, ...props }: HTMLAttributes<HTMLHeadingElement>) {
  return (
    <h2
      className={cn("font-semibold text-[15px] leading-none tracking-tight", className)}
      data-slot="card-title"
      {...props}
    />
  );
}

export function CardDescription({ className, ...props }: HTMLAttributes<HTMLParagraphElement>) {
  return (
    <p
      className={cn("text-muted-foreground text-sm", className)}
      data-slot="card-description"
      {...props}
    />
  );
}

/** Trailing controls in the header row, opposite the title. */
export function CardAction({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("ml-auto flex items-center gap-2", className)}
      data-slot="card-action"
      {...props}
    />
  );
}

export function CardContent({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("p-5 pt-0", className)} data-slot="card-content" {...props} />;
}

export function CardFooter({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("flex items-center gap-2 border-border border-t p-5", className)}
      data-slot="card-footer"
      {...props}
    />
  );
}
