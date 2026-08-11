"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { useLocale, useTranslations } from "next-intl";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import type { Listing } from "@/lib/operations";
import { useSession } from "@/lib/session";

type CustomerImport = {
  id: string;
  source: string;
  status: string;
  total: number;
  valid: number;
  conflicts: number;
  invalid: number;
  errorSummary: Record<string, unknown>;
  resumable: boolean;
  startedAt: string;
  completedAt?: string;
};

/**
 * Customer imports and the customer export.
 *
 * An import is previewed before it is applied, and the preview is where the
 * duplicate detection lands: a record that collides with somebody who already
 * exists is counted as a conflict rather than silently merged or silently
 * skipped. The operator sees how many are clean, how many collide, and how many
 * cannot be read at all, before anything is written.
 */
export function ImportPanel() {
  const translate = useTranslations("admin.imports");
  const locale = useLocale();
  const { can } = useSession();

  const { data, isLoading } = useSWR<Listing<CustomerImport>, ApiError>(
    "/v1/panel/imports",
    fetcher,
    // A run in progress moves while an operator watches it.
    { refreshInterval: 5000 },
  );
  const items = data?.items ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {can("customers.read") && (
          <Button asChild className="self-start" size="sm" variant="outline">
            <a href="/v1/panel/customers/export">{translate("export")}</a>
          </Button>
        )}
        <p className="text-muted-foreground text-xs">{translate("exportNotice")}</p>

        {isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : items.length === 0 ? (
          <StateNotice
            description={translate("empty.description")}
            title={translate("empty.title")}
            variant="empty"
          />
        ) : (
          <div className="flex flex-col gap-2 border-border border-t pt-3">
            {items.map((run) => (
              <div className="flex flex-col gap-1" key={run.id}>
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <span className="flex items-center gap-2">
                    <span className="font-mono text-[11px]">{run.id.slice(0, 8)}</span>
                    <Badge
                      variant={
                        run.status === "completed"
                          ? "success"
                          : run.status === "failed"
                            ? "danger"
                            : "neutral"
                      }
                    >
                      {translate(`status.${run.status}`)}
                    </Badge>
                    {/* A stored cursor is what makes a run resumable: it names
                        where the source was left off, so a run interrupted at
                        record 40,000 does not start again at one. */}
                    {run.resumable && <Badge variant="warning">{translate("resumable")}</Badge>}
                  </span>
                  <span className="flex items-center gap-3 text-sm tabular-nums">
                    <span>{translate("counts.total", { count: run.total })}</span>
                    <span className="text-success-foreground">
                      {translate("counts.valid", { count: run.valid })}
                    </span>
                    <span className="text-warning-foreground">
                      {translate("counts.conflicts", { count: run.conflicts })}
                    </span>
                    <span className="text-danger-foreground">
                      {translate("counts.invalid", { count: run.invalid })}
                    </span>
                  </span>
                </div>
                <span className="font-mono text-[11px] text-muted-foreground">
                  {new Date(run.startedAt).toLocaleString(locale)}
                  {run.completedAt ? ` → ${new Date(run.completedAt).toLocaleString(locale)}` : ""}
                </span>
                {Object.keys(run.errorSummary ?? {}).length > 0 && (
                  // The validation errors are a class-and-count summary rather
                  // than a list of rows: an operator needs to know that 812
                  // records had no Telegram identifier, not to scroll past 812
                  // identical lines.
                  <span className="text-danger-foreground text-xs">
                    {Object.entries(run.errorSummary)
                      .map(([code, count]) => `${translate(`error.${code}`)}: ${String(count)}`)
                      .join(" · ")}
                  </span>
                )}
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
