import { ChevronLeft, ChevronRight } from "lucide-react";
import type { ReactNode } from "react";

import { Button } from "./button";
import { cn } from "./lib/utils";

export type CursorPaginationProps = {
  className?: string;
  /** Rendered between the two controls, e.g. "1–25 of 340". */
  summary?: ReactNode;
  hasNext: boolean;
  hasPrevious: boolean;
  labels: { next: string; previous: string };
  onNext: () => void;
  onPrevious: () => void;
  pending?: boolean;
};

/**
 * Cursor pagination controls. The API exposes opaque cursors rather than page
 * offsets, so there are no numbered pages to jump to — only the two directions
 * the caller currently holds a cursor for.
 */
export function CursorPagination({
  className,
  hasNext,
  hasPrevious,
  labels,
  onNext,
  onPrevious,
  pending,
  summary,
}: CursorPaginationProps) {
  return (
    <nav
      aria-label={labels.next}
      className={cn("flex items-center justify-between gap-4 px-1 py-2", className)}
      data-slot="cursor-pagination"
    >
      <p className="text-muted-foreground text-xs" data-numeric>
        {summary}
      </p>
      <div className="flex items-center gap-2">
        <Button
          disabled={!hasPrevious || pending}
          onClick={onPrevious}
          size="sm"
          type="button"
          variant="outline"
        >
          <ChevronLeft />
          {labels.previous}
        </Button>
        <Button
          disabled={!hasNext || pending}
          onClick={onNext}
          size="sm"
          type="button"
          variant="outline"
        >
          {labels.next}
          <ChevronRight />
        </Button>
      </div>
    </nav>
  );
}
