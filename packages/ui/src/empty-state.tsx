import type { ComponentProps, ReactNode } from "react";

import { cn } from "./lib/utils";

/*
 * `title` is omitted from the div props before being redeclared: the DOM
 * attribute is a plain string, and leaving it in would narrow this slot to a
 * string rather than the rich node the heading actually renders.
 */
export type EmptyStateProps = Omit<ComponentProps<"div">, "title"> & {
  /** Optional action, typically the button that resolves the emptiness. */
  action?: ReactNode;
  description?: ReactNode;
  icon?: ReactNode;
  title: ReactNode;
};

/**
 * Shared treatment for the "nothing here" cases the panel must always render
 * explicitly: an empty list, a filter that matched nothing, or a feature the
 * installation has not configured yet.
 */
export function EmptyState({
  action,
  className,
  description,
  icon,
  title,
  ...props
}: EmptyStateProps) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-3 rounded-xl border border-border border-dashed",
        "px-6 py-12 text-center",
        className,
      )}
      data-slot="empty-state"
      {...props}
    >
      {icon && (
        <div
          aria-hidden="true"
          className="flex size-10 items-center justify-center rounded-full bg-secondary text-muted-foreground [&_svg]:size-5"
        >
          {icon}
        </div>
      )}
      <div className="flex flex-col gap-1">
        <p className="font-medium text-[15px] text-foreground">{title}</p>
        {description && <p className="text-muted-foreground text-sm">{description}</p>}
      </div>
      {action}
    </div>
  );
}
