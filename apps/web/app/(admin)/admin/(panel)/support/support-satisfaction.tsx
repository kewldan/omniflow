"use client";

import type { PanelSupportSatisfaction } from "@omniflow/api-client/generated";
import { Card } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useLocale, useTranslations } from "next-intl";
import { useMemo } from "react";
import useSWR from "swr";

import { type ApiError, fetcher, toQuery } from "@/lib/api";

/**
 * Customer satisfaction over the same window as the workload above it.
 *
 * Once a ticket is resolved the bot asks its customer for a mark from one to
 * five, once. CSAT is the share of fours and fives; the response rate says how
 * much of the desk the figure speaks for.
 */
export function SupportSatisfactionCard({ windowDays }: { windowDays: number }) {
  const translate = useTranslations("admin.supportSatisfaction");
  const locale = useLocale();
  const query = useMemo(() => {
    const until = new Date();
    const since = new Date(until.getTime() - windowDays * 24 * 60 * 60 * 1000);
    return toQuery({ since: since.toISOString(), until: until.toISOString() });
  }, [windowDays]);
  const { data, isLoading } = useSWR<PanelSupportSatisfaction, ApiError>(
    `/v1/panel/reports/support-satisfaction${query}`,
    fetcher,
  );

  if (isLoading || !data) {
    return <Skeleton className="h-32 w-full" />;
  }

  const figures = [
    { key: "csat", value: percent(data.csat, locale) },
    {
      key: "average",
      value:
        data.average === undefined
          ? "—"
          : data.average.toLocaleString(locale, { maximumFractionDigits: 2 }),
    },
    { key: "ratings", value: String(data.ratings) },
    { key: "responseRate", value: percent(data.responseRate, locale) },
  ] as const;
  const largest = Math.max(1, ...data.distribution);

  return (
    <Card className="flex flex-col gap-4 p-4">
      <div>
        <h3 className="font-semibold">{translate("title")}</h3>
        <p className="text-muted-foreground text-sm">{translate("description")}</p>
      </div>
      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
        {figures.map((figure) => (
          <div className="flex flex-col gap-1" key={figure.key}>
            <span className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
              {translate(`figures.${figure.key}.label`)}
            </span>
            <span className="font-semibold text-2xl tabular-nums">{figure.value}</span>
            <span className="text-muted-foreground text-xs">
              {translate(`figures.${figure.key}.definition`)}
            </span>
          </div>
        ))}
      </div>
      <ul aria-label={translate("distribution")} className="flex flex-col gap-1">
        {data.distribution.map((count, index) => {
          const mark = index + 1;
          return (
            <li className="flex items-center gap-2 text-sm" key={mark}>
              <span className="w-8 tabular-nums">{mark} ⭐</span>
              <span aria-hidden className="bg-muted h-2 flex-1 overflow-hidden rounded-full">
                <span
                  className="bg-primary block h-full rounded-full"
                  style={{ width: `${Math.round((count / largest) * 100)}%` }}
                />
              </span>
              <span className="w-10 text-right tabular-nums">{count}</span>
            </li>
          );
        })}
      </ul>
      {data.operators.length > 0 ? (
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("operator")}</TableHead>
                <TableHead className="text-right">{translate("figures.ratings.label")}</TableHead>
                <TableHead className="text-right">{translate("figures.csat.label")}</TableHead>
                <TableHead className="text-right">{translate("figures.average.label")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.operators.map((operator) => (
                <TableRow key={operator.operatorId}>
                  <TableCell>{operator.displayName}</TableCell>
                  <TableCell className="text-right tabular-nums">{operator.ratings}</TableCell>
                  <TableCell className="text-right tabular-nums">
                    {percent(operator.csat, locale)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {operator.average === undefined
                      ? "—"
                      : operator.average.toLocaleString(locale, { maximumFractionDigits: 2 })}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : null}
    </Card>
  );
}

function percent(value: number | undefined, locale: string): string {
  if (value === undefined) {
    return "—";
  }
  return value.toLocaleString(locale, { style: "percent", maximumFractionDigits: 1 });
}
