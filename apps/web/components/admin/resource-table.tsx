"use client";

import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { CursorPagination } from "@omniflow/ui/pagination";
import { SkeletonTable } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";
import type { ReactNode } from "react";

import { StateNotice } from "@/components/admin/state-notice";
import type { ApiError } from "@/lib/api";

/**
 * The shared table shell every operations list uses.
 *
 * Each list page has the same five states — first load, unreachable, empty,
 * empty-because-filtered, and ready — and getting one of them subtly wrong on
 * one page is how a panel starts feeling unreliable. Putting them here means a
 * page supplies its columns and its rows, and inherits the rest.
 *
 * A background refresh dims the table rather than replacing it, so an operator
 * keeps reading what is already on screen.
 */
export function ResourceTable<Row>({
  columns,
  rows,
  renderRow,
  error,
  loading,
  validating,
  filtersActive,
  onClearFilters,
  onRetry,
  pagination,
  emptyTitle,
  emptyDescription,
}: {
  columns: ReactNode[];
  rows: Row[] | undefined;
  renderRow: (row: Row, index: number) => ReactNode;
  error?: ApiError | null;
  loading: boolean;
  validating?: boolean;
  filtersActive?: boolean;
  onClearFilters?: () => void;
  onRetry?: () => void;
  pagination?: {
    hasNext: boolean;
    hasPrevious: boolean;
    onNext: () => void;
    onPrevious: () => void;
    page: number;
  };
  emptyTitle?: string;
  emptyDescription?: string;
}) {
  const translate = useTranslations("admin.operations");
  const items = rows ?? [];

  if (loading && !rows) {
    return (
      <Card className="overflow-hidden">
        <SkeletonTable columns={columns.length} rows={8} />
      </Card>
    );
  }

  if (error) {
    return (
      <StateNotice
        action={
          onRetry ? (
            <Button onClick={onRetry} size="sm" variant="outline">
              {translate("retry")}
            </Button>
          ) : undefined
        }
        description={translate("errorDescription")}
        title={translate("error")}
        variant="danger"
      />
    );
  }

  if (items.length === 0) {
    /*
     * "Nothing matched your filter" and "there is nothing here" are different
     * situations. Showing the same copy for both is what makes an operator
     * believe a working page is broken.
     */
    return (
      <StateNotice
        action={
          filtersActive && onClearFilters ? (
            <Button onClick={onClearFilters} size="sm" variant="outline">
              {translate("clearFilters")}
            </Button>
          ) : undefined
        }
        description={
          filtersActive
            ? translate("noMatchesDescription")
            : (emptyDescription ?? translate("emptyDescription"))
        }
        title={filtersActive ? translate("noMatches") : (emptyTitle ?? translate("empty"))}
        variant={filtersActive ? "filtered" : "empty"}
      />
    );
  }

  return (
    <Card className="overflow-hidden">
      <div
        aria-busy={validating || undefined}
        className={validating ? "opacity-60 transition-opacity" : "transition-opacity"}
      >
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                {columns.map((column, index) => (
                  // Column headers are static per page, so the index is a stable key.
                  // biome-ignore lint/suspicious/noArrayIndexKey: static column list
                  <TableHead key={index}>{column}</TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>{items.map(renderRow)}</TableBody>
          </Table>
        </div>
      </div>

      {pagination && (
        <div className="border-border border-t px-2">
          <CursorPagination
            hasNext={pagination.hasNext}
            hasPrevious={pagination.hasPrevious}
            labels={{ next: translate("next"), previous: translate("previous") }}
            onNext={pagination.onNext}
            onPrevious={pagination.onPrevious}
            pending={validating}
            summary={translate("shown", { count: items.length, page: pagination.page })}
          />
        </div>
      )}
    </Card>
  );
}

/** The standard page header: an eyebrow, a title, a description, and actions. */
export function PageHeader({
  actions,
  description,
  eyebrow,
  title,
}: {
  actions?: ReactNode;
  description?: ReactNode;
  eyebrow?: ReactNode;
  title: ReactNode;
}) {
  return (
    <header className="flex flex-wrap items-start justify-between gap-3">
      <div className="flex flex-col gap-1">
        {eyebrow && (
          <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.16em]">
            {eyebrow}
          </p>
        )}
        <h1 className="font-bold text-2xl tracking-tight">{title}</h1>
        {description && <p className="max-w-2xl text-muted-foreground text-sm">{description}</p>}
      </div>
      {actions}
    </header>
  );
}
