"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { TableCell, TableRow } from "@omniflow/ui/table";
import { RotateCcw } from "lucide-react";
import Link from "next/link";
import { useLocale, useTranslations } from "next-intl";
import { useId } from "react";
import useSWR from "swr";

import { PageHeader, ResourceTable } from "@/components/admin/resource-table";
import { type ApiError, fetcher, toQuery } from "@/lib/api";
import type { CustomerSummary, Page } from "@/lib/operations";
import { usePreferences } from "@/lib/use-preferences";
import { useUrlFilters } from "@/lib/use-url-filters";

const STATUSES = ["active", "suspended", "deleted"] as const;
const SEGMENTS = ["subscribed", "lapsed", "never_purchased", "flagged"] as const;

const STATUS_VARIANT: Record<string, "success" | "warning" | "danger"> = {
  active: "success",
  suspended: "warning",
  deleted: "danger",
};

/**
 * Customer search.
 *
 * The single search box takes whichever identifier the operator has — a
 * customer identifier, a Telegram identifier, or a Remnawave username — and the
 * API decides which one it is from its shape. There is deliberately no
 * free-text search: contact values are stored as ciphertext plus a fingerprint
 * precisely so they cannot be trawled.
 */
export function CustomerSearch() {
  const translate = useTranslations("admin.customers");
  const locale = useLocale();
  const statusId = useId();
  const segmentId = useId();
  const queryId = useId();

  const { filters, cursorStack, setFilter, reset, goNext, goPrevious, hasPrevious } = useUrlFilters(
    ["status", "segment", "q"],
  );
  const { preferences } = usePreferences();
  const pageSize = preferences.pageSize || 25;

  const query = toQuery({
    cursor: filters.cursor,
    pageSize,
    q: filters.q,
    segment: filters.segment,
    status: filters.status,
  });

  const { data, error, isLoading, isValidating, mutate } = useSWR<Page<CustomerSummary>, ApiError>(
    `/v1/panel/customers${query}`,
    fetcher,
    { keepPreviousData: true },
  );

  const filtersActive = Boolean(filters.status || filters.segment || filters.q);

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />

      <Card className="p-4">
        <div className="flex flex-wrap items-end gap-3">
          <div className="flex min-w-64 flex-1 flex-col gap-1.5">
            <Label htmlFor={queryId}>{translate("filters.query")}</Label>
            <Input
              className="font-mono text-[13px]"
              defaultValue={filters.q}
              id={queryId}
              onBlur={(event) => setFilter("q", event.target.value.trim())}
              placeholder={translate("filters.queryPlaceholder")}
            />
          </div>

          <div className="flex min-w-36 flex-col gap-1.5">
            <Label htmlFor={statusId}>{translate("filters.status")}</Label>
            <Select
              onValueChange={(value) => setFilter("status", value === "all" ? "" : value)}
              value={filters.status || "all"}
            >
              <SelectTrigger id={statusId}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{translate("filters.any")}</SelectItem>
                {STATUSES.map((status) => (
                  <SelectItem key={status} value={status}>
                    {translate(`status.${status}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="flex min-w-44 flex-col gap-1.5">
            <Label htmlFor={segmentId}>{translate("filters.segment")}</Label>
            <Select
              onValueChange={(value) => setFilter("segment", value === "all" ? "" : value)}
              value={filters.segment || "all"}
            >
              <SelectTrigger id={segmentId}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{translate("filters.any")}</SelectItem>
                {SEGMENTS.map((segment) => (
                  <SelectItem key={segment} value={segment}>
                    {translate(`segments.${segment}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {filtersActive && (
            <Button onClick={reset} size="sm" variant="ghost">
              <RotateCcw />
              {translate("filters.clear")}
            </Button>
          )}
        </div>
      </Card>

      <ResourceTable<CustomerSummary>
        columns={[
          translate("columns.customer"),
          translate("columns.status"),
          translate("columns.telegram"),
          translate("columns.locale"),
          translate("columns.createdAt"),
        ]}
        error={error}
        filtersActive={filtersActive}
        loading={isLoading}
        onClearFilters={reset}
        onRetry={() => mutate()}
        pagination={{
          hasNext: Boolean(data?.nextCursor),
          hasPrevious,
          onNext: () => goNext(data?.nextCursor),
          onPrevious: goPrevious,
          page: cursorStack.length + 1,
        }}
        renderRow={(customer) => (
          <TableRow key={customer.id}>
            <TableCell className="font-mono text-[12px]">
              <Link
                className="underline-offset-2 hover:underline"
                href={`/admin/customers/${customer.id}`}
              >
                {customer.id.slice(0, 8)}
              </Link>
            </TableCell>
            <TableCell>
              <Badge variant={STATUS_VARIANT[customer.status] ?? "neutral"}>
                {translate(`status.${customer.status}`)}
              </Badge>
            </TableCell>
            <TableCell className="font-mono text-[12px] text-muted-foreground" data-numeric>
              {customer.telegramId ?? "—"}
            </TableCell>
            <TableCell className="text-muted-foreground">{customer.locale}</TableCell>
            <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
              {new Date(customer.createdAt).toLocaleDateString(locale)}
            </TableCell>
          </TableRow>
        )}
        rows={data?.items ?? undefined}
        validating={isValidating}
      />
    </div>
  );
}
