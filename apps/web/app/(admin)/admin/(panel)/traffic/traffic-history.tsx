"use client";

import type { PanelTrafficHistory } from "@omniflow/api-client/generated";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { ChartFigure, chartAxis, chartBarTooltip, chartColor, chartGrid } from "@omniflow/ui/chart";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useLocale, useTranslations } from "next-intl";
import { useMemo, useState } from "react";
import { Bar, BarChart, CartesianGrid, Tooltip, XAxis, YAxis } from "recharts";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher, toQuery } from "@/lib/api";
import { formatBytes } from "@/lib/operations";

const WINDOWS = [7, 30, 90] as const;

/**
 * Traffic per day, from the hourly samples the worker records of Remnawave's
 * node counters. The live screen above stays live; this is the history
 * Remnawave does not keep, observed and never acted on.
 */
export function TrafficHistoryCard() {
  const translate = useTranslations("admin.trafficHistory");
  const locale = useLocale();
  const [days, setDays] = useState<number>(30);
  const query = useMemo(() => {
    const until = new Date();
    const since = new Date(until.getTime() - days * 24 * 60 * 60 * 1000);
    return toQuery({
      since: since.toISOString(),
      timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
      until: until.toISOString(),
    });
  }, [days]);
  const { data, error, isLoading } = useSWR<PanelTrafficHistory, ApiError>(
    `/v1/panel/reports/traffic/history${query}`,
    fetcher,
  );

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-5">
        <div className="flex flex-wrap gap-2">
          {WINDOWS.map((window) => (
            <Button
              key={window}
              onClick={() => setDays(window)}
              size="sm"
              type="button"
              variant={days === window ? "default" : "outline"}
            >
              {translate("window", { days: window })}
            </Button>
          ))}
        </div>
        {error ? (
          <StateNotice description={error.message} title={translate("failed")} variant="danger" />
        ) : isLoading || !data ? (
          <Skeleton className="h-56 w-full" />
        ) : data.nodes.length === 0 ? (
          <StateNotice description={translate("emptyDescription")} title={translate("empty")} />
        ) : (
          <>
            <ChartFigure
              description={translate("chartDescription")}
              empty={translate("empty")}
              isEmpty={data.totals.every((day) => day.usedBytes === 0)}
              table={
                <details className="text-sm">
                  <summary className="cursor-pointer text-muted-foreground text-xs">
                    {translate("showFigures")}
                  </summary>
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>{translate("day")}</TableHead>
                        <TableHead className="text-right">{translate("used")}</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {data.totals.map((day) => (
                        <TableRow key={day.day}>
                          <TableCell className="font-mono text-xs">{day.day}</TableCell>
                          <TableCell className="text-right tabular-nums">
                            {formatBytes(day.usedBytes, locale)}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </details>
              }
              title={translate("chart")}
            >
              <BarChart data={data.totals.map((day) => ({ ...day, fill: chartColor("chart-1") }))}>
                <CartesianGrid {...chartGrid} />
                <XAxis {...chartAxis} dataKey="day" />
                <YAxis
                  {...chartAxis}
                  tickFormatter={(value: number) => formatBytes(value, locale)}
                  width={72}
                />
                <Tooltip
                  {...chartBarTooltip}
                  formatter={(value) => [formatBytes(Number(value), locale), translate("used")]}
                />
                <Bar dataKey="usedBytes" radius={4} />
              </BarChart>
            </ChartFigure>
            <div className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{translate("node")}</TableHead>
                    <TableHead className="text-right">{translate("total")}</TableHead>
                    <TableHead className="text-right">{translate("perDay")}</TableHead>
                    <TableHead className="text-right">{translate("peakOnline")}</TableHead>
                    <TableHead className="text-right">{translate("limit")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.nodes.map((node) => (
                    <TableRow key={node.nodeUuid}>
                      <TableCell>
                        {node.nodeName}
                        {node.countryCode ? (
                          <span className="text-muted-foreground ml-2 font-mono text-xs">
                            {node.countryCode}
                          </span>
                        ) : null}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">
                        {formatBytes(node.totalBytes, locale)}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">
                        {node.days.length > 0
                          ? formatBytes(Math.round(node.totalBytes / node.days.length), locale)
                          : "—"}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">{node.peakOnline}</TableCell>
                      <TableCell className="text-right tabular-nums">
                        {node.limitBytes > 0 ? formatBytes(node.limitBytes, locale) : "—"}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}
