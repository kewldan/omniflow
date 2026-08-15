"use client";

import { Badge } from "@omniflow/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { ChartFigure, chartAxis, chartColor, chartGrid } from "@omniflow/ui/chart";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useLocale, useTranslations } from "next-intl";
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import { formatMoney, type PaymentHealthReport, type ProviderHealthLine } from "@/lib/operations";

/**
 * Payment health per provider.
 *
 * With four bundled adapters, an acquirer that starts refusing cards shows up as
 * support tickets and a growing stuck-payment queue rather than as a number that
 * moved. This is that number.
 *
 * Two rates, and the distinction is the whole point. **Settlement** is
 * settled ÷ (settled + failed): how often the provider completes a payment it
 * was asked to take. **Completion** adds the customers who walked away, which is
 * the funnel rather than the acquirer. Collapsing them produces a figure that
 * drops every time a campaign reaches people who were never going to buy.
 *
 * Intents still in flight are in neither denominator and are shown apart. One
 * created five minutes ago has not failed.
 */
export function PaymentHealthScreen({ query }: { query: string }) {
  const translate = useTranslations("admin.paymentHealth");
  const locale = useLocale();

  const { data, error, isLoading } = useSWR<PaymentHealthReport, ApiError>(
    `/v1/panel/reports/payments${query}`,
    fetcher,
  );

  if (error) {
    return <StateNotice description={error.message} title={translate("failed")} variant="danger" />;
  }
  if (isLoading || !data) {
    return <Skeleton className="h-64 w-full" />;
  }
  if (data.providers.length === 0) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>{translate("title")}</CardTitle>
          <CardDescription>{translate("description")}</CardDescription>
        </CardHeader>
        <CardContent>
          <StateNotice description={translate("emptyDescription")} title={translate("empty")} />
        </CardContent>
      </Card>
    );
  }

  return (
    <>
      <Card>
        <CardContent className="pt-6">
          <OutcomeChart providers={data.providers} />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{translate("title")}</CardTitle>
          <CardDescription>{translate("description")}</CardDescription>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("provider")}</TableHead>
                <TableHead>{translate("currency")}</TableHead>
                <TableHead className="text-right">{translate("settlement")}</TableHead>
                <TableHead className="text-right">{translate("completion")}</TableHead>
                <TableHead className="text-right">{translate("settled")}</TableHead>
                <TableHead className="text-right">{translate("failed")}</TableHead>
                <TableHead className="text-right">{translate("abandoned")}</TableHead>
                <TableHead className="text-right">{translate("open")}</TableHead>
                <TableHead className="text-right">{translate("median")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.providers.map((line) => (
                <TableRow key={`${line.provider}-${line.currency}`}>
                  <TableCell>{translate(`adapter.${line.provider}`)}</TableCell>
                  <TableCell>
                    <Badge variant="neutral">{line.currency}</Badge>
                  </TableCell>
                  <TableCell className="text-right">
                    <RateCell line={line} rate={line.settlementRate} />
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {percent(line.completionRate)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {line.settled}
                    <span className="block text-subtle-foreground text-xs">
                      {formatMoney(line.settledMinor, line.currency, locale)}
                    </span>
                  </TableCell>
                  <TableCell className="text-right tabular-nums">{line.failed}</TableCell>
                  <TableCell className="text-right tabular-nums">{line.abandoned}</TableCell>
                  <TableCell className="text-right tabular-nums">
                    {line.stillOpen}
                    {line.stillOpen > 0 ? (
                      <span className="block text-subtle-foreground text-xs">
                        {translate("oldest", { age: duration(line.oldestOpenSeconds) })}
                      </span>
                    ) : null}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {duration(line.medianSettleSeconds)}
                    <span className="block text-subtle-foreground text-xs">
                      p95 {duration(line.p95SettleSeconds)}
                    </span>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      {data.webhooks.length > 0 ? (
        <Card>
          <CardHeader>
            <CardTitle>{translate("webhooks.title")}</CardTitle>
            <CardDescription>{translate("webhooks.description")}</CardDescription>
          </CardHeader>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("provider")}</TableHead>
                  <TableHead className="text-right">{translate("webhooks.received")}</TableHead>
                  <TableHead className="text-right">{translate("webhooks.processed")}</TableHead>
                  <TableHead className="text-right">{translate("webhooks.failed")}</TableHead>
                  <TableHead className="text-right">{translate("webhooks.rejected")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.webhooks.map((line) => (
                  <TableRow key={line.provider}>
                    <TableCell>{translate(`adapter.${line.provider}`)}</TableCell>
                    <TableCell className="text-right tabular-nums">{line.received}</TableCell>
                    <TableCell className="text-right tabular-nums">{line.processed}</TableCell>
                    <TableCell className="text-right tabular-nums">{line.failed}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {line.rejected > 0 ? (
                        <span className="font-medium text-warning">{line.rejected}</span>
                      ) : (
                        line.rejected
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      ) : null}

      <DailyTable report={data} />
    </>
  );
}

/**
 * What happened to every intent, per provider, as one stacked bar each.
 *
 * Stacked is right here and wrong on the dashboard's revenue chart, and the
 * difference is whether the total means anything. Settled, failed, and abandoned
 * are disjoint fates of the same population, so the bar's length is the number
 * of intents that reached one — a figure worth seeing. Intents still in flight
 * are left out: an intent created five minutes ago has not failed, and giving it
 * a segment would make a busy afternoon look like a broken acquirer.
 *
 * Providers are read on the vertical axis because their names are words. A
 * category axis of words along the bottom is where labels start rotating.
 */
function OutcomeChart({ providers }: { providers: ProviderHealthLine[] }) {
  const translate = useTranslations("admin.paymentHealth");

  const rows = providers.map((line) => ({
    abandoned: line.abandoned,
    failed: line.failed,
    key: `${line.provider}-${line.currency}`,
    label: `${translate(`adapter.${line.provider}`)} · ${line.currency}`,
    settled: line.settled,
  }));

  const series = [
    { fill: chartColor("chart-1"), key: "settled" as const },
    { fill: chartColor("chart-2"), key: "failed" as const },
    { fill: chartColor("chart-3"), key: "abandoned" as const },
  ];

  return (
    <ChartFigure
      description={translate("outcomes.description")}
      empty={translate("outcomes.empty")}
      height={Math.max(180, rows.length * 46)}
      isEmpty={rows.every((row) => row.settled + row.failed + row.abandoned === 0)}
      table={
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("provider")}</TableHead>
              {series.map((entry) => (
                <TableHead className="text-right" key={entry.key}>
                  {translate(entry.key)}
                </TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <TableRow key={row.key}>
                <TableCell>{row.label}</TableCell>
                {series.map((entry) => (
                  <TableCell className="text-right tabular-nums" key={entry.key}>
                    <span className="inline-flex items-center justify-end gap-2">
                      <span
                        aria-hidden
                        className="size-2.5 shrink-0 rounded-[3px]"
                        style={{ background: entry.fill }}
                      />
                      {row[entry.key]}
                    </span>
                  </TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      }
      title={translate("outcomes.title")}
    >
      <BarChart data={rows} layout="vertical" margin={{ left: 8, right: 16, top: 4 }}>
        <CartesianGrid {...chartGrid} horizontal={false} vertical />
        <XAxis {...chartAxis} allowDecimals={false} type="number" />
        <YAxis {...chartAxis} dataKey="label" type="category" width={140} />
        {series.map((entry, index) => (
          <Bar
            dataKey={entry.key}
            fill={entry.fill}
            key={entry.key}
            maxBarSize={24}
            radius={index === series.length - 1 ? [0, 4, 4, 0] : undefined}
            stackId="outcome"
          />
        ))}
      </BarChart>
    </ChartFigure>
  );
}

/**
 * A settlement rate with a tone, and a sample size beside it.
 *
 * A rate over two payments is not a signal, so the count travels with the
 * figure: 50% of four is a coincidence and 50% of four hundred is an incident,
 * and colouring the first red would train an operator to ignore the second.
 */
function RateCell({ line, rate }: { line: ProviderHealthLine; rate?: number }) {
  const translate = useTranslations("admin.paymentHealth");
  const decided = line.settled + line.failed;
  const tone =
    rate === undefined || decided < 20
      ? "text-foreground"
      : rate < 0.8
        ? "text-destructive"
        : rate < 0.95
          ? "text-warning"
          : "text-success";

  return (
    <span className="tabular-nums">
      <span className={`font-medium ${tone}`}>{percent(rate)}</span>
      <span className="block text-subtle-foreground text-xs">
        {translate("sample", { count: decided })}
      </span>
    </span>
  );
}

/** Settled and failed per day, which is what turns a rate into a trend. */
function DailyTable({ report }: { report: PaymentHealthReport }) {
  const translate = useTranslations("admin.paymentHealth");
  if (report.byDay.length === 0) {
    return null;
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("daily.title")}</CardTitle>
        <CardDescription>{translate("daily.description")}</CardDescription>
      </CardHeader>
      <CardContent className="overflow-x-auto">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("daily.day")}</TableHead>
              <TableHead>{translate("provider")}</TableHead>
              <TableHead className="text-right">{translate("settled")}</TableHead>
              <TableHead className="text-right">{translate("failed")}</TableHead>
              <TableHead className="text-right">{translate("settlement")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {report.byDay.map((day) => (
              <TableRow key={`${day.day}-${day.provider}`}>
                <TableCell className="font-mono text-xs">{day.day}</TableCell>
                <TableCell>{translate(`adapter.${day.provider}`)}</TableCell>
                <TableCell className="text-right tabular-nums">{day.settled}</TableCell>
                <TableCell className="text-right tabular-nums">{day.failed}</TableCell>
                <TableCell className="text-right tabular-nums">
                  {percent(
                    day.settled + day.failed > 0
                      ? day.settled / (day.settled + day.failed)
                      : undefined,
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}

/** An em dash rather than 0% when nothing reached a decision. */
function percent(value?: number): string {
  if (value === undefined || value === null) {
    return "—";
  }
  return `${Math.round(value * 1000) / 10}%`;
}

/** Seconds as the coarsest unit that still says something useful. */
function duration(seconds: number): string {
  if (seconds <= 0) {
    return "—";
  }
  if (seconds < 90) {
    return `${Math.round(seconds)}s`;
  }
  if (seconds < 5400) {
    return `${Math.round(seconds / 60)}m`;
  }
  if (seconds < 172800) {
    return `${Math.round(seconds / 3600)}h`;
  }
  return `${Math.round(seconds / 86400)}d`;
}
