"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { ArrowRight } from "lucide-react";
import Link from "next/link";
import { useLocale, useTranslations } from "next-intl";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import {
  type Dashboard,
  formatBytes,
  formatDuration,
  formatMoney,
  type Metric,
} from "@/lib/operations";
import { useSession } from "@/lib/session";

/**
 * The operations dashboard.
 *
 * Two things distinguish it from a wall of numbers. Every tile carries the
 * definition of what was counted, because the failure mode of an operations
 * dashboard is not a wrong number but a right one somebody read as meaning
 * something else. And the "needs action" list is derived from the same figures
 * the tiles show, so an alert can never contradict the tile beside it.
 */
export default function AdminHome() {
  const translate = useTranslations("admin");
  const locale = useLocale();
  const { session, can } = useSession();

  const { data, error, isLoading, mutate } = useSWR<Dashboard, ApiError>(
    can("system.read") ? "/v1/panel/overview/dashboard" : null,
    fetcher,
    // A dashboard that silently ages is worse than one that is obviously stale,
    // so it refreshes when the operator returns to the tab.
    { revalidateOnFocus: true, keepPreviousData: true },
  );

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        description={translate("home.description")}
        eyebrow={translate("home.eyebrow")}
        title={translate("home.greeting", { name: session?.account.displayName ?? "" })}
      />

      {!can("system.read") ? (
        <Card className="p-6">
          <StateNotice
            description={translate("home.noDashboardDescription")}
            title={translate("home.noDashboard")}
            variant="forbidden"
          />
        </Card>
      ) : error ? (
        <Card className="p-6">
          <StateNotice
            action={
              <Button onClick={() => mutate()} size="sm" variant="outline">
                {translate("operations.retry")}
              </Button>
            }
            description={translate("operations.errorDescription")}
            title={translate("operations.error")}
            variant="danger"
          />
        </Card>
      ) : isLoading && !data ? (
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {[0, 1, 2, 3, 4, 5, 6, 7].map((slot) => (
            <Skeleton className="h-24 w-full" key={slot} />
          ))}
        </div>
      ) : data ? (
        <>
          <Attention items={data.attention ?? []} />

          <p className="text-muted-foreground text-xs">
            {translate("dashboard.freshness", {
              at: new Date(data.generatedAt).toLocaleString(locale),
              timezone: data.timezone,
              window: formatDuration(data.windowSeconds),
            })}
          </p>

          <MetricGroup metrics={data.customers} title={translate("dashboard.groups.customers")} />
          <MetricGroup
            metrics={data.subscriptions}
            title={translate("dashboard.groups.subscriptions")}
          />
          <MetricGroup metrics={data.payments} title={translate("dashboard.groups.payments")} />

          <section aria-labelledby="revenue-heading" className="flex flex-col gap-3">
            <h2 className="font-semibold text-[15px] tracking-tight" id="revenue-heading">
              {translate("dashboard.groups.revenue")}
            </h2>
            <Card>
              <CardHeader>
                <CardDescription>{translate("dashboard.revenueDefinition")}</CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                {(data.revenue ?? []).length === 0 ? (
                  <p className="text-muted-foreground text-sm">
                    {translate("dashboard.noRevenue")}
                  </p>
                ) : (
                  (data.revenue ?? []).map((line) => (
                    <div
                      className="flex flex-wrap items-baseline gap-x-6 gap-y-1"
                      key={line.currency}
                    >
                      <Badge variant="neutral">{line.currency}</Badge>
                      <Figure
                        label={translate("dashboard.revenue.paid")}
                        value={formatMoney(line.paidMinor, line.currency, locale)}
                      />
                      <Figure
                        label={translate("dashboard.revenue.wallet")}
                        value={formatMoney(line.walletMinor, line.currency, locale)}
                      />
                      <Figure
                        label={translate("dashboard.revenue.refunded")}
                        value={formatMoney(line.refundedMinor, line.currency, locale)}
                      />
                      <Figure
                        label={translate("dashboard.revenue.orders")}
                        value={String(line.orderCount)}
                      />
                    </div>
                  ))
                )}
              </CardContent>
            </Card>
          </section>

          <MetricGroup metrics={data.support} title={translate("dashboard.groups.support")} />
          <MetricGroup metrics={data.operations} title={translate("dashboard.groups.operations")} />
        </>
      ) : null}
    </div>
  );
}

function Figure({ label, value }: { label: string; value: string }) {
  return (
    <span className="flex flex-col">
      <span className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
        {label}
      </span>
      <span className="font-semibold tabular-nums">{value}</span>
    </span>
  );
}

/**
 * The short list of things that need somebody to act.
 *
 * It renders nothing when the installation is healthy, rather than a row of
 * zeroes an operator learns to scroll past. Each entry deep-links into the
 * ordinary panel surface that resolves it, so the fix goes through the usual
 * confirmation and audit rather than around it.
 */
function Attention({
  items,
}: {
  items: { key: string; severity: string; count: number; href: string }[];
}) {
  const translate = useTranslations("admin.dashboard");
  if (items.length === 0) {
    return null;
  }
  return (
    <section aria-labelledby="attention-heading" className="flex flex-col gap-3">
      <h2 className="font-semibold text-[15px] tracking-tight" id="attention-heading">
        {translate("attention")}
      </h2>
      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
        {items.map((item) => (
          <Card className="p-3" key={item.key}>
            <div className="flex items-center justify-between gap-3">
              <div className="flex flex-col gap-0.5">
                <span className="font-medium text-sm">
                  {translate(`attentionItems.${item.key}`)}
                </span>
                <span className="font-semibold text-lg tabular-nums">{item.count}</span>
              </div>
              <div className="flex items-center gap-2">
                <Badge variant={item.severity === "alert" ? "danger" : "warning"}>
                  {translate(`severity.${item.severity}`)}
                </Badge>
                <Button asChild size="icon-sm" variant="ghost">
                  <Link aria-label={translate("open")} href={item.href}>
                    <ArrowRight />
                  </Link>
                </Button>
              </div>
            </div>
          </Card>
        ))}
      </div>
    </section>
  );
}

/**
 * One group of metric tiles.
 *
 * The definition sits under the number rather than behind a tooltip: it is the
 * part an operator has to read once and then trusts, and a tooltip is exactly
 * the affordance nobody discovers.
 */
function MetricGroup({ metrics, title }: { metrics: Metric[]; title: string }) {
  const translate = useTranslations("admin.dashboard");
  const headingId = `metrics-${title.replace(/\s+/g, "-").toLowerCase()}`;

  return (
    <section aria-labelledby={headingId} className="flex flex-col gap-3">
      <h2 className="font-semibold text-[15px] tracking-tight" id={headingId}>
        {title}
      </h2>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {metrics.map((metric) => (
          <Card className="p-4" key={metric.key}>
            <CardTitle className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
              {translate(`metrics.${metric.key}`)}
            </CardTitle>
            <p className="mt-1 flex items-baseline gap-2">
              <span className="font-semibold text-2xl tabular-nums">{renderMetric(metric)}</span>
              <Comparison metric={metric} />
            </p>
            <p className="mt-1 text-muted-foreground text-xs">
              {translate(`definitions.${metric.definition}`)}
            </p>
          </Card>
        ))}
      </div>
    </section>
  );
}

/**
 * The change against the preceding window of the same length.
 *
 * Only windowed measures carry one. A point-in-time total compared against
 * "the same total a month ago" would be answering a question the query did not
 * ask, so those simply have no arrow.
 */
function Comparison({ metric }: { metric: Metric }) {
  const translate = useTranslations("admin.dashboard");
  if (metric.comparison === undefined) {
    return null;
  }
  const delta = metric.value - metric.comparison;
  if (delta === 0) {
    return <span className="text-muted-foreground text-xs">{translate("unchanged")}</span>;
  }
  return (
    <span className="text-muted-foreground text-xs tabular-nums">
      {delta > 0 ? "+" : ""}
      {delta.toLocaleString()} {translate("versusPrevious")}
    </span>
  );
}

/**
 * Renders a metric in the unit it is actually measured in.
 *
 * A byte count shown as a bare integer and a duration shown as a bare integer
 * look identical and mean nothing, so the two that are not plain counts are
 * formatted by their key.
 */
function renderMetric(metric: Metric): string {
  if (metric.key === "observedTrafficBytes") {
    return formatBytes(metric.value);
  }
  if (metric.key === "outboxOldestAgeSeconds") {
    return metric.value === 0 ? "—" : formatDuration(metric.value);
  }
  return metric.value.toLocaleString();
}
