import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

/**
 * Loading placeholder. It is `aria-hidden` because the surrounding region
 * announces its own busy state; a screen reader should hear "loading" once,
 * not once per shimmering block.
 */
export function Skeleton({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      aria-hidden="true"
      className={cn("animate-pulse rounded-md bg-secondary", className)}
      data-slot="skeleton"
      {...props}
    />
  );
}

/** Widths cycle so a placeholder table reads like real, ragged content. */
const CELL_WIDTHS = ["8rem", "6rem", "5rem", "7rem"];

/** Row of skeleton cells sized for the admin tables. */
export function SkeletonTable({ columns = 4, rows = 5 }: { columns?: number; rows?: number }) {
  /*
   * The placeholder grid is static and never reorders, so the coordinate pair
   * is a stable identity for each cell.
   */
  const cells = Array.from({ length: rows }, (_, row) =>
    Array.from({ length: columns }, (_, column) => ({
      id: `${row}:${column}`,
      width: CELL_WIDTHS[column % CELL_WIDTHS.length],
    })),
  );

  return (
    <div aria-busy="true" className="flex flex-col gap-px" data-slot="skeleton-table">
      {cells.map((row) => (
        <div className="flex items-center gap-4 px-4 py-3" key={row[0].id}>
          {row.map((cell) => (
            <Skeleton className="h-4 flex-1" key={cell.id} style={{ maxWidth: cell.width }} />
          ))}
        </div>
      ))}
    </div>
  );
}
