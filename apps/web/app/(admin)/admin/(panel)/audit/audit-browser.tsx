"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { CursorPagination } from "@omniflow/ui/pagination";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { SkeletonTable } from "@omniflow/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
  TableSortButton,
} from "@omniflow/ui/table";
import { Download, RotateCcw } from "lucide-react";
import { useTranslations } from "next-intl";
import { useId } from "react";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher, toQuery } from "@/lib/api";
import { useSession } from "@/lib/session";
import { usePreferences } from "@/lib/use-preferences";
import { useUrlFilters } from "@/lib/use-url-filters";

const CATEGORIES = [
  "authentication",
  "authorization",
  "configuration",
  "customer",
  "financial",
  "support",
  "marketing",
  "system",
] as const;

const OUTCOMES = ["success", "failure", "denied"] as const;

type AuditEvent = {
  id: string;
  occurredAt: string;
  actorType: string;
  actorId?: string;
  action: string;
  category: string;
  outcome: "success" | "failure" | "denied";
  targetType: string;
  targetId: string;
  reason?: string;
  requestId?: string;
};

type AuditPage = { items: AuditEvent[]; nextCursor?: string };

const OUTCOME_VARIANT: Record<AuditEvent["outcome"], "danger" | "success" | "warning"> = {
  denied: "warning",
  failure: "danger",
  success: "success",
};

export function AuditBrowser() {
  const translate = useTranslations("admin.audit");
  const { can } = useSession();
  const categoryId = useId();
  const outcomeId = useId();
  const actionId = useId();

  /*
   * Filters live in the URL, so an operator can bookmark a view, share it with
   * a colleague, and use the browser's back button to step through the trail
   * they already looked at.
   */
  const { filters, cursorStack, setFilter, reset, goNext, goPrevious, hasPrevious } = useUrlFilters(
    ["category", "outcome", "action", "sort"],
  );
  const { preferences, save: savePreferences } = usePreferences();

  /*
   * The URL wins over the stored preference, so a shared link always shows what
   * the sender saw. The preference only supplies the default for a fresh visit.
   */
  const sort = filters.sort || preferences.auditSort || "desc";
  const pageSize = preferences.pageSize || 25;

  function toggleSort() {
    const next = sort === "desc" ? "asc" : "desc";
    // Cursors encode a position in one ordering, so flipping direction resets
    // paging rather than carrying an invalid cursor across.
    setFilter("sort", next);
    savePreferences({ auditSort: next });
  }

  const query = toQuery({
    action: filters.action,
    category: filters.category,
    cursor: filters.cursor,
    outcome: filters.outcome,
    pageSize,
    sort,
  });

  const { data, error, isLoading, isValidating, mutate } = useSWR<AuditPage, ApiError>(
    `/v1/panel/audit${query}`,
    fetcher,
    { keepPreviousData: true },
  );

  const filtersActive = Boolean(filters.category || filters.outcome || filters.action);
  const events = data?.items ?? [];

  return (
    <div className="flex flex-col gap-5">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex flex-col gap-1">
          <h1 className="font-bold text-2xl tracking-tight">{translate("title")}</h1>
          <p className="max-w-2xl text-muted-foreground text-sm">{translate("description")}</p>
        </div>
        {can("audit.export") && (
          <Button asChild size="sm" variant="outline">
            {/*
              A plain link rather than a fetch: the response is a streamed CSV
              attachment, so the browser's own download handling is correct.
            */}
            <a href={`/v1/panel/audit/export${query}`}>
              <Download />
              {translate("export")}
            </a>
          </Button>
        )}
      </header>

      <Card className="p-4">
        <div className="flex flex-wrap items-end gap-3">
          <div className="flex min-w-40 flex-col gap-1.5">
            <Label htmlFor={categoryId}>{translate("filters.category")}</Label>
            <Select
              onValueChange={(value) => setFilter("category", value === "all" ? "" : value)}
              value={filters.category || "all"}
            >
              <SelectTrigger id={categoryId}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{translate("filters.any")}</SelectItem>
                {CATEGORIES.map((category) => (
                  <SelectItem key={category} value={category}>
                    {translate(`categories.${category}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="flex min-w-36 flex-col gap-1.5">
            <Label htmlFor={outcomeId}>{translate("filters.outcome")}</Label>
            <Select
              onValueChange={(value) => setFilter("outcome", value === "all" ? "" : value)}
              value={filters.outcome || "all"}
            >
              <SelectTrigger id={outcomeId}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">{translate("filters.any")}</SelectItem>
                {OUTCOMES.map((outcome) => (
                  <SelectItem key={outcome} value={outcome}>
                    {translate(`outcomes.${outcome}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="flex min-w-48 flex-1 flex-col gap-1.5">
            <Label htmlFor={actionId}>{translate("filters.action")}</Label>
            <Input
              className="font-mono text-[13px]"
              defaultValue={filters.action}
              id={actionId}
              onBlur={(event) => setFilter("action", event.target.value.trim())}
              placeholder="admin.login"
            />
          </div>

          {filtersActive && (
            <Button onClick={reset} size="sm" variant="ghost">
              <RotateCcw />
              {translate("filters.clear")}
            </Button>
          )}
        </div>
      </Card>

      <Card className="overflow-hidden">
        {isLoading && !data ? (
          <SkeletonTable columns={5} rows={8} />
        ) : error ? (
          <div className="p-6">
            <StateNotice
              action={
                <Button onClick={() => mutate()} size="sm" variant="outline">
                  {translate("retry")}
                </Button>
              }
              description={translate("errorDescription")}
              title={translate("error")}
              variant="danger"
            />
          </div>
        ) : events.length === 0 ? (
          <div className="p-6">
            {/*
              "Nothing matched your filter" and "the trail is empty" are
              different situations and get different copy and a different way out.
            */}
            <StateNotice
              action={
                filtersActive ? (
                  <Button onClick={reset} size="sm" variant="outline">
                    {translate("filters.clear")}
                  </Button>
                ) : undefined
              }
              description={
                filtersActive ? translate("noMatchesDescription") : translate("emptyDescription")
              }
              title={filtersActive ? translate("noMatches") : translate("empty")}
              variant={filtersActive ? "filtered" : "empty"}
            />
          </div>
        ) : (
          <>
            {/*
              A background refresh dims the table rather than replacing it, so
              the operator keeps reading what is already on screen.
            */}
            <div
              className={isValidating ? "opacity-60 transition-opacity" : "transition-opacity"}
              aria-busy={isValidating || undefined}
            >
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead aria-sort={sort === "asc" ? "ascending" : "descending"}>
                      <TableSortButton
                        direction={sort === "asc" ? "asc" : "desc"}
                        onSort={toggleSort}
                      >
                        {translate("columns.occurredAt")}
                      </TableSortButton>
                    </TableHead>
                    <TableHead>{translate("columns.action")}</TableHead>
                    <TableHead>{translate("columns.category")}</TableHead>
                    <TableHead>{translate("columns.outcome")}</TableHead>
                    <TableHead>{translate("columns.target")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {events.map((event) => (
                    <TableRow key={event.id}>
                      <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
                        {new Date(event.occurredAt).toLocaleString()}
                      </TableCell>
                      <TableCell className="font-mono text-[12px]">{event.action}</TableCell>
                      <TableCell className="text-muted-foreground">
                        {translate(`categories.${event.category}`)}
                      </TableCell>
                      <TableCell>
                        <Badge variant={OUTCOME_VARIANT[event.outcome]}>
                          {translate(`outcomes.${event.outcome}`)}
                        </Badge>
                      </TableCell>
                      <TableCell className="max-w-56 truncate font-mono text-[11px] text-muted-foreground">
                        {event.targetType}:{event.targetId}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>

            <div className="border-border border-t px-2">
              <CursorPagination
                hasNext={Boolean(data?.nextCursor)}
                hasPrevious={hasPrevious}
                labels={{ next: translate("next"), previous: translate("previous") }}
                onNext={() => goNext(data?.nextCursor)}
                onPrevious={goPrevious}
                pending={isValidating}
                summary={translate("shown", { count: events.length, page: cursorStack.length + 1 })}
              />
            </div>
          </>
        )}
      </Card>
    </div>
  );
}
