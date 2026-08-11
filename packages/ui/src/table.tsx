import { ArrowDown, ArrowUp, ChevronsUpDown } from "lucide-react";
import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

export function Table({ className, ...props }: ComponentProps<"table">) {
  return (
    <div className="relative w-full overflow-x-auto" data-slot="table-container">
      <table
        className={cn("w-full caption-bottom border-collapse text-sm", className)}
        data-slot="table"
        {...props}
      />
    </div>
  );
}

export function TableHeader({ className, ...props }: ComponentProps<"thead">) {
  return (
    <thead
      className={cn("[&_tr]:border-border [&_tr]:border-b", className)}
      data-slot="table-header"
      {...props}
    />
  );
}

export function TableBody({ className, ...props }: ComponentProps<"tbody">) {
  return (
    <tbody
      className={cn("[&_tr:last-child]:border-0", className)}
      data-slot="table-body"
      {...props}
    />
  );
}

export function TableFooter({ className, ...props }: ComponentProps<"tfoot">) {
  return (
    <tfoot
      className={cn("border-border border-t bg-muted/40 font-medium", className)}
      data-slot="table-footer"
      {...props}
    />
  );
}

export function TableRow({ className, ...props }: ComponentProps<"tr">) {
  return (
    <tr
      className={cn(
        "border-border border-b transition-colors hover:bg-muted/50 data-[state=selected]:bg-muted",
        className,
      )}
      data-slot="table-row"
      {...props}
    />
  );
}

export function TableHead({ className, ...props }: ComponentProps<"th">) {
  return (
    <th
      className={cn(
        "h-10 whitespace-nowrap px-4 text-left align-middle font-medium text-muted-foreground text-xs",
        "[&:has([role=checkbox])]:w-px [&:has([role=checkbox])]:pr-0",
        className,
      )}
      data-slot="table-head"
      {...props}
    />
  );
}

export function TableCell({ className, ...props }: ComponentProps<"td">) {
  return (
    <td
      className={cn(
        "px-4 py-3 align-middle [&:has([role=checkbox])]:w-px [&:has([role=checkbox])]:pr-0",
        className,
      )}
      data-slot="table-cell"
      {...props}
    />
  );
}

export function TableCaption({ className, ...props }: ComponentProps<"caption">) {
  return (
    <caption
      className={cn("mt-4 text-muted-foreground text-sm", className)}
      data-slot="table-caption"
      {...props}
    />
  );
}

export type SortDirection = "asc" | "desc";

export type TableSortButtonProps = Omit<ComponentProps<"button">, "onClick"> & {
  /** Current direction for this column, or null when another column is sorted. */
  direction: SortDirection | null;
  onSort: () => void;
};

/**
 * Sortable column header. The direction is exposed through `aria-sort` on the
 * parent cell by the caller, and repeated visually by the icon, so the state is
 * available to both assistive technology and sighted operators.
 */
export function TableSortButton({
  children,
  className,
  direction,
  onSort,
  ...props
}: TableSortButtonProps) {
  const Icon = direction === "asc" ? ArrowUp : direction === "desc" ? ArrowDown : ChevronsUpDown;
  return (
    <button
      className={cn(
        "-mx-2 inline-flex items-center gap-1.5 rounded-sm px-2 py-1 font-medium",
        "hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/40 focus-visible:outline-none",
        direction ? "text-foreground" : "text-muted-foreground",
        className,
      )}
      onClick={onSort}
      type="button"
      {...props}
    >
      {children}
      <Icon aria-hidden="true" className="size-3.5 opacity-70" />
    </button>
  );
}
