"use client";

import type {
  PanelCohort,
  PanelRecurringRevenueDay,
  PanelSubscriptionReport,
} from "@omniflow/api-client/generated";
import { Badge } from "@omniflow/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import {
  CHART_SERIES,
  ChartFigure,
  chartAxis,
  chartColor,
  chartGrid,
  chartTooltip,
} from "@omniflow/ui/chart";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useLocale, useTranslations } from "next-intl";
import { Area, AreaChart, CartesianGrid, Line, LineChart, Tooltip, XAxis, YAxis } from "recharts";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import { formatMoney } from "@/lib/operations";

import { ChurnReasonsCard, ForecastCard, MovementCard } from "./retention";

/**
 * The subscription base over time: who holds access and why, what it is worth
 * every month, how much of it renews, and what each cohort of payers became.
 *
 * Shares the period controls above it with the sales tab, for the same reason
 * payment health does. Money stays per currency throughout; imported periods,
 * which carry no price here, are valued at list price and always shown apart
 * as an estimate.
 */
export function SubscriptionReportScreen({ query }: { query: string }) {
  const translate = useTranslations("admin.subscriptionReport");
  const locale = useLocale();
  const { data, error, isLoading } = useSWR<PanelSubscriptionReport, ApiError>(
    `/v1/panel/reports/subscriptions${query}`,
    fetcher,
  );

  if (error) {
    return <StateNotice description={error.message} title={translate("failed")} variant="danger" />;
  }
  if (isLoading || !data) {
    return <Skeleton className="h-96 w-full" />;
  }

  const latest = data.base.at(-1);
  const latestMrr = data.mrr.filter((line) => line.day === data.latestDay);

  return (
    <>
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Tile
          hint={translate("tiles.paidHint")}
          label={translate("tiles.paid")}
          value={latest ? String(latest.paid + latest.imported) : "—"}
        />
        <Tile
          hint={translate("tiles.trialHint")}
          label={translate("tiles.trial")}
          value={latest ? String(latest.trial) : "—"}
        />
        <Tile
          hint={translate("tiles.renewalHint", { days: data.renewals.graceDays })}
          label={translate("tiles.renewal")}
          value={percent(data.renewals.renewalRate, locale)}
        />
        <Tile
          hint={translate("tiles.churnHint")}
          label={translate("tiles.churn")}
          value={percent(data.renewals.churnRate, locale)}
        />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{translate("mrr.title")}</CardTitle>
          <CardDescription>{translate("mrr.description")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-6">
          {latestMrr.length === 0 ? (
            <StateNotice description={translate("snapshotsPending")} title={translate("empty")} />
          ) : (
            <div className="flex flex-wrap gap-6">
              {latestMrr.map((line) => (
                <div className="flex flex-col" key={line.currency}>
                  <span className="text-2xl font-semibold tabular-nums">
                    {formatMoney(line.mrrMinor, line.currency, locale)}
                  </span>
                  {line.estimatedMinor > 0 ? (
                    <span className="text-muted-foreground text-xs">
                      {translate("mrr.estimated", {
                        amount: formatMoney(line.estimatedMinor, line.currency, locale),
                      })}
                    </span>
                  ) : null}
                </div>
              ))}
            </div>
          )}
          <MrrCharts locale={locale} mrr={data.mrr} />
          <BaseChart report={data} />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{translate("plans.title")}</CardTitle>
          <CardDescription>
            {translate("plans.description", { day: data.latestDay ?? "—" })}
          </CardDescription>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("plans.plan")}</TableHead>
                <TableHead>{translate("plans.period")}</TableHead>
                <TableHead>{translate("plans.kind")}</TableHead>
                <TableHead className="text-right">{translate("plans.active")}</TableHead>
                <TableHead className="text-right">{translate("plans.mrr")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.plans.map((line) => (
                <TableRow
                  key={`${line.planCode}-${line.billingPeriod}-${line.kind}-${line.currency}`}
                >
                  <TableCell className="font-mono text-xs">{line.planCode}</TableCell>
                  <TableCell>{line.billingPeriod}</TableCell>
                  <TableCell>
                    <Badge variant="neutral">{translate(`kinds.${line.kind}`)}</Badge>
                  </TableCell>
                  <TableCell className="text-right tabular-nums">{line.active}</TableCell>
                  <TableCell className="text-right tabular-nums">
                    {line.currency ? formatMoney(line.mrrMinor, line.currency, locale) : "—"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{translate("renewals.title")}</CardTitle>
          <CardDescription>
            {translate("renewals.description", { days: data.renewals.graceDays })}
          </CardDescription>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("plans.plan")}</TableHead>
                <TableHead>{translate("plans.period")}</TableHead>
                <TableHead className="text-right">{translate("renewals.ended")}</TableHead>
                <TableHead className="text-right">{translate("renewals.renewed")}</TableHead>
                <TableHead className="text-right">{translate("renewals.churned")}</TableHead>
                <TableHead className="text-right">{translate("renewals.pending")}</TableHead>
                <TableHead className="text-right">{translate("renewals.rate")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.renewals.lines.map((line) => (
                <TableRow key={`${line.planCode}-${line.billingPeriod}-${line.kind}`}>
                  <TableCell className="font-mono text-xs">
                    {line.planCode}
                    {line.kind === "imported" ? (
                      <Badge className="ml-2" variant="neutral">
                        {translate("kinds.imported")}
                      </Badge>
                    ) : null}
                  </TableCell>
                  <TableCell>{line.billingPeriod}</TableCell>
                  <TableCell className="text-right tabular-nums">{line.ended}</TableCell>
                  <TableCell className="text-right tabular-nums">{line.renewed}</TableCell>
                  <TableCell className="text-right tabular-nums">{line.churned}</TableCell>
                  <TableCell className="text-right tabular-nums">{line.pending}</TableCell>
                  <TableCell className="text-right tabular-nums">
                    {percent(line.renewalRate, locale)}
                  </TableCell>
                </TableRow>
              ))}
              <TableRow>
                <TableCell className="font-medium" colSpan={2}>
                  {translate("renewals.total")}
                </TableCell>
                <TableCell className="text-right tabular-nums">{data.renewals.ended}</TableCell>
                <TableCell className="text-right tabular-nums">{data.renewals.renewed}</TableCell>
                <TableCell className="text-right tabular-nums">{data.renewals.churned}</TableCell>
                <TableCell className="text-right tabular-nums">{data.renewals.pending}</TableCell>
                <TableCell className="text-right tabular-nums">
                  {percent(data.renewals.renewalRate, locale)}
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{translate("autoRenew.title")}</CardTitle>
          <CardDescription>
            {translate("autoRenew.description", { enabled: data.autoRenew.enabled })}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4 overflow-x-auto">
          {data.autoRenew.lines.length === 0 ? (
            <StateNotice description={translate("autoRenew.empty")} title={translate("empty")} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("autoRenew.funding")}</TableHead>
                  <TableHead className="text-right">{translate("autoRenew.cycles")}</TableHead>
                  <TableHead className="text-right">{translate("autoRenew.firstTry")}</TableHead>
                  <TableHead className="text-right">{translate("autoRenew.recovered")}</TableHead>
                  <TableHead className="text-right">{translate("autoRenew.lost")}</TableHead>
                  <TableHead className="text-right">{translate("autoRenew.inProgress")}</TableHead>
                  <TableHead className="text-right">{translate("autoRenew.success")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.autoRenew.lines.map((line) => (
                  <TableRow key={line.funding}>
                    <TableCell>{translate(`autoRenew.fundings.${line.funding}`)}</TableCell>
                    <TableCell className="text-right tabular-nums">{line.cycles}</TableCell>
                    <TableCell className="text-right tabular-nums">{line.firstTry}</TableCell>
                    <TableCell className="text-right tabular-nums">{line.recovered}</TableCell>
                    <TableCell className="text-right tabular-nums">{line.lost}</TableCell>
                    <TableCell className="text-right tabular-nums">{line.inProgress}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {percent(line.successRate, locale)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
          {data.autoRenew.failureCodes.length > 0 ? (
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <span className="text-muted-foreground">{translate("autoRenew.failures")}</span>
              {data.autoRenew.failureCodes.map((code) => (
                <Badge key={code.code} variant="neutral">
                  <span className="font-mono">{code.code}</span> · {code.attempts}
                </Badge>
              ))}
            </div>
          ) : null}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{translate("perCustomer.title")}</CardTitle>
          <CardDescription>{translate("perCustomer.description")}</CardDescription>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("perCustomer.currency")}</TableHead>
                <TableHead className="text-right">{translate("perCustomer.revenue")}</TableHead>
                <TableHead className="text-right">{translate("perCustomer.payers")}</TableHead>
                <TableHead className="text-right">{translate("perCustomer.arppu")}</TableHead>
                <TableHead className="text-right">{translate("perCustomer.subscribers")}</TableHead>
                <TableHead className="text-right">{translate("perCustomer.arpu")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.perCustomer.map((line) => (
                <TableRow key={line.currency}>
                  <TableCell>
                    <Badge variant="neutral">{line.currency}</Badge>
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {formatMoney(line.paidMinor, line.currency, locale)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">{line.payers}</TableCell>
                  <TableCell className="text-right tabular-nums">
                    {money(line.arppuMinor, line.currency, locale)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {line.averageSubscribers === undefined
                      ? "—"
                      : line.averageSubscribers.toLocaleString(locale, {
                          maximumFractionDigits: 1,
                        })}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {money(line.arpuMinor, line.currency, locale)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <MovementCard lines={data.movement} locale={locale} />
      <ForecastCard forecast={data.forecast} locale={locale} />
      <ChurnReasonsCard lines={data.churnReasons} locale={locale} />
      <CohortCard cohorts={data.cohorts} locale={locale} />
    </>
  );
}

function Tile({ hint, label, value }: { hint: string; label: string; value: string }) {
  return (
    <Card>
      <CardContent className="flex flex-col gap-1 pt-6">
        <span className="text-muted-foreground text-sm">{label}</span>
        <span className="text-2xl font-semibold tabular-nums">{value}</span>
        <span className="text-muted-foreground text-xs">{hint}</span>
      </CardContent>
    </Card>
  );
}

/** Recurring revenue by day, one chart per currency. */
function MrrCharts({ locale, mrr }: { locale: string; mrr: PanelRecurringRevenueDay[] }) {
  const translate = useTranslations("admin.subscriptionReport");
  const currencies = [...new Set(mrr.map((line) => line.currency))].sort();
  if (currencies.length === 0) {
    return null;
  }
  return (
    <div className="grid gap-6 lg:grid-cols-2">
      {currencies.map((currency) => {
        const days = mrr.filter((line) => line.currency === currency);
        return (
          <ChartFigure
            description={translate("mrr.chartDescription")}
            empty={translate("empty")}
            isEmpty={days.every((day) => day.mrrMinor === 0 && day.estimatedMinor === 0)}
            key={currency}
            table={
              <details className="text-sm">
                <summary className="cursor-pointer text-muted-foreground text-xs">
                  {translate("showFigures")}
                </summary>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{translate("day")}</TableHead>
                      <TableHead className="text-right">{translate("mrr.paid")}</TableHead>
                      <TableHead className="text-right">{translate("mrr.estimate")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {days.map((day) => (
                      <TableRow key={day.day}>
                        <TableCell className="font-mono text-xs">{day.day}</TableCell>
                        <TableCell className="text-right tabular-nums">
                          {formatMoney(day.mrrMinor, currency, locale)}
                        </TableCell>
                        <TableCell className="text-right tabular-nums">
                          {formatMoney(day.estimatedMinor, currency, locale)}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </details>
            }
            title={
              <>
                {translate("mrr.chart")} <Badge variant="neutral">{currency}</Badge>
              </>
            }
          >
            <LineChart data={days}>
              <CartesianGrid {...chartGrid} />
              <XAxis {...chartAxis} dataKey="day" />
              <YAxis
                {...chartAxis}
                tickFormatter={(value: number) =>
                  formatMoney(value, currency, locale, { compact: true })
                }
                width={72}
              />
              <Tooltip
                {...chartTooltip}
                formatter={(value, name) => [
                  formatMoney(Number(value), currency, locale),
                  name === "mrrMinor" ? translate("mrr.paid") : translate("mrr.estimate"),
                ]}
              />
              <Line
                dataKey="mrrMinor"
                dot={false}
                stroke={chartColor("chart-1")}
                strokeWidth={2}
                type="monotone"
              />
              <Line
                dataKey="estimatedMinor"
                dot={false}
                stroke={chartColor("chart-3")}
                strokeDasharray="4 4"
                strokeWidth={2}
                type="monotone"
              />
            </LineChart>
          </ChartFigure>
        );
      })}
    </div>
  );
}

/** Subscriptions by kind per day, stacked. Counts, so currencies do not matter. */
function BaseChart({ report }: { report: PanelSubscriptionReport }) {
  const translate = useTranslations("admin.subscriptionReport");
  const kinds = ["paid", "imported", "trial", "free"] as const;
  const charted = ["paid", "imported", "trial"] as const;
  return (
    <ChartFigure
      description={translate("base.description")}
      empty={translate("snapshotsPending")}
      isEmpty={report.base.length === 0}
      table={
        <details className="text-sm">
          <summary className="cursor-pointer text-muted-foreground text-xs">
            {translate("showFigures")}
          </summary>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("day")}</TableHead>
                {kinds.map((kind) => (
                  <TableHead className="text-right" key={kind}>
                    {translate(`kinds.${kind}`)}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {report.base.map((day) => (
                <TableRow key={day.day}>
                  <TableCell className="font-mono text-xs">{day.day}</TableCell>
                  {kinds.map((kind) => (
                    <TableCell className="text-right tabular-nums" key={kind}>
                      {day[kind]}
                    </TableCell>
                  ))}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </details>
      }
      title={translate("base.title")}
    >
      <AreaChart data={report.base}>
        <CartesianGrid {...chartGrid} />
        <XAxis {...chartAxis} dataKey="day" />
        <YAxis {...chartAxis} allowDecimals={false} width={48} />
        <Tooltip
          {...chartTooltip}
          formatter={(value, name) => [String(value), translate(`kinds.${String(name)}`)]}
        />
        {/* Three series, one per palette colour; free grants stay in the table. */}
        {charted.map((kind, index) => (
          <Area
            dataKey={kind}
            fill={chartColor(CHART_SERIES[index])}
            fillOpacity={0.35}
            key={kind}
            stackId="base"
            stroke={chartColor(CHART_SERIES[index])}
            type="monotone"
          />
        ))}
      </AreaChart>
    </ChartFigure>
  );
}

/**
 * Cohorts of payers by the month of their first payment. Each cell is the
 * share of the cohort that held paid access in that month — access rather than
 * payment, so a yearly subscriber reads as retained all year.
 */
function CohortCard({ cohorts, locale }: { cohorts: PanelCohort[]; locale: string }) {
  const translate = useTranslations("admin.subscriptionReport");
  const width = Math.max(0, ...cohorts.map((cohort) => cohort.retention.length));
  const offsets = Array.from({ length: width }, (_, index) => index);
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("cohorts.title")}</CardTitle>
        <CardDescription>{translate("cohorts.description")}</CardDescription>
      </CardHeader>
      <CardContent className="overflow-x-auto">
        {cohorts.length === 0 ? (
          <StateNotice description={translate("cohorts.empty")} title={translate("empty")} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("cohorts.month")}</TableHead>
                <TableHead className="text-right">{translate("cohorts.customers")}</TableHead>
                {offsets.map((offset) => (
                  <TableHead className="text-right" key={offset}>
                    {translate("cohorts.offset", { offset })}
                  </TableHead>
                ))}
                <TableHead className="text-right">{translate("cohorts.ltv")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {cohorts.map((cohort) => (
                <TableRow key={cohort.month}>
                  <TableCell className="font-mono text-xs">{cohort.month.slice(0, 7)}</TableCell>
                  <TableCell className="text-right tabular-nums">{cohort.customers}</TableCell>
                  {offsets.map((offset) => {
                    const cell = cohort.retention.find((entry) => entry.offset === offset);
                    return (
                      <TableCell
                        className="text-right tabular-nums"
                        key={offset}
                        style={
                          cell?.rate === undefined
                            ? undefined
                            : {
                                backgroundColor: `color-mix(in oklab, ${chartColor("chart-1")} ${Math.round(cell.rate * 60)}%, transparent)`,
                              }
                        }
                      >
                        {cell ? percent(cell.rate, locale) : ""}
                      </TableCell>
                    );
                  })}
                  <TableCell className="text-right tabular-nums">
                    {cohort.revenue.length === 0
                      ? "—"
                      : cohort.revenue
                          .map((line) => money(line.ltvMinor, line.currency, locale))
                          .join(" · ")}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

function percent(value: number | undefined, locale: string): string {
  if (value === undefined) {
    return "—";
  }
  return value.toLocaleString(locale, { style: "percent", maximumFractionDigits: 1 });
}

function money(minor: number | undefined, currency: string, locale: string): string {
  return minor === undefined ? "—" : formatMoney(minor, currency, locale);
}
