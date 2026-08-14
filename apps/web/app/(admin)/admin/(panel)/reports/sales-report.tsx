"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { DateTimeField } from "@omniflow/ui/date-time-field";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { Download } from "lucide-react";
import dynamic from "next/dynamic";
import { useLocale, useTranslations } from "next-intl";
import { useId, useMemo, useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher, toQuery } from "@/lib/api";
import { formatMoney, type SalesReport } from "@/lib/operations";

/**
 * The daily chart is loaded on demand.
 *
 * Recharts is the single heaviest dependency either panel pulls, and this screen
 * also carries the date picker's calendar. Together they put the route over the
 * first-load JavaScript budget, which is a gate rather than a guideline. Loading
 * the chart after the page means the tables — which carry the same figures, and
 * are the accessible equivalent the chart is required to have anyway — arrive
 * first.
 */
const DailyChart = dynamic(() => import("./daily-chart").then((module) => module.DailyChart), {
  ssr: false,
});

/** The ranges an operator actually asks for, as offsets from now. */
const PRESETS = [
  { days: 7, key: "week" },
  { days: 30, key: "month" },
  { days: 90, key: "quarter" },
  { days: 365, key: "year" },
] as const;

/**
 * Sales over a period the operator chooses.
 *
 * The dashboard's fixed thirty days stay where they are, because a dashboard
 * whose window moves cannot be compared between two visits. This screen answers
 * the questions that window cannot: how a quarter went, which plan is selling,
 * what share of the money is renewals rather than new business, and whether the
 * trials turn into anything.
 *
 * Two things the API decides and this screen only renders. Provider money and
 * wallet credit are never added, because the balance was already revenue when it
 * was funded. And refunds are reported on the day they were issued rather than
 * against the sale they reverse, so a closed month stays closed.
 */
export function SalesReportScreen() {
  const translate = useTranslations("admin.reports");
  const locale = useLocale();

  // A local wall-clock string, which is what DateTimeField produces and what the
  // operator means: they are asking about their own days, not about UTC.
  const [since, setSince] = useState(() => localInput(daysAgo(30)));
  const [until, setUntil] = useState(() => localInput(new Date()));
  const [currency, setCurrency] = useState("");

  const timezone = useMemo(() => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC", []);

  const query = toQuery({
    currency: currency || undefined,
    since: since ? new Date(since).toISOString() : undefined,
    timezone,
    until: until ? new Date(until).toISOString() : undefined,
  });
  const { data, error, isLoading } = useSWR<SalesReport, ApiError>(
    `/v1/panel/reports/sales${query}`,
    fetcher,
  );

  const currencies = useMemo(() => {
    const seen = new Set((data?.byOperation ?? []).map((line) => line.currency));
    return [...seen].sort();
  }, [data]);

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />

      <Card>
        <CardHeader>
          <CardTitle>{translate("period.title")}</CardTitle>
          <CardDescription>{translate("period.description", { timezone })}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <div className="flex flex-wrap gap-2">
            {PRESETS.map((preset) => (
              <Button
                key={preset.key}
                onClick={() => {
                  setSince(localInput(daysAgo(preset.days)));
                  setUntil(localInput(new Date()));
                }}
                size="sm"
                variant="outline"
              >
                {translate(`period.${preset.key}`)}
              </Button>
            ))}
          </div>
          <div className="grid gap-4 sm:grid-cols-3">
            <PeriodField
              label={translate("period.since")}
              onChange={setSince}
              translate={translate}
              value={since}
            />
            <PeriodField
              label={translate("period.until")}
              onChange={setUntil}
              translate={translate}
              value={until}
            />
            <CurrencyFilter
              currencies={currencies}
              label={translate("period.currency")}
              onChange={setCurrency}
              value={currency}
            />
          </div>
          <div>
            <Button asChild size="sm" variant="secondary">
              {/* An ordinary link rather than a fetch: the response is a file,
                  and the browser's own download is what an operator expects. */}
              <a href={`/v1/panel/reports/sales/export${query}`}>
                <Download aria-hidden />
                {translate("export")}
              </a>
            </Button>
          </div>
        </CardContent>
      </Card>

      {error ? (
        <StateNotice description={error.message} title={translate("failed")} variant="danger" />
      ) : null}

      {isLoading || !data ? (
        <Skeleton className="h-96 w-full" />
      ) : (
        <>
          <TrialCard report={data} />
          <OperationCard locale={locale} report={data} />
          <PlanCard locale={locale} report={data} />
          <DailyChart locale={locale} report={data} />
          <RefundCard locale={locale} report={data} />
        </>
      )}
    </div>
  );
}

function PeriodField({
  label,
  onChange,
  translate,
  value,
}: {
  label: string;
  onChange: (value: string) => void;
  translate: (key: string) => string;
  value: string;
}) {
  const id = useId();
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={id}>{label}</Label>
      <DateTimeField
        hourLabel={translate("period.hour")}
        id={id}
        minuteLabel={translate("period.minute")}
        onChange={onChange}
        placeholder={label}
        value={value}
      />
    </div>
  );
}

function CurrencyFilter({
  currencies,
  label,
  onChange,
  value,
}: {
  currencies: string[];
  label: string;
  onChange: (value: string) => void;
  value: string;
}) {
  const translate = useTranslations("admin.reports");
  const id = useId();
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={id}>{label}</Label>
      <Select onValueChange={(next) => onChange(next === "all" ? "" : next)} value={value || "all"}>
        <SelectTrigger id={id}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all">{translate("period.allCurrencies")}</SelectItem>
          {currencies.map((entry) => (
            <SelectItem key={entry} value={entry}>
              {entry}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}

/**
 * Trial conversion, with the shape of the measure stated beside it.
 *
 * The denominator is trials claimed inside the period and the numerator counts
 * conversions at any later time, so a period ending today reads low by
 * construction. Saying so is the difference between a number somebody can use
 * and a number somebody will misread once and distrust forever.
 */
function TrialCard({ report }: { report: SalesReport }) {
  const translate = useTranslations("admin.reports");
  const rate =
    report.trials.trials > 0
      ? Math.round((report.trials.converted / report.trials.trials) * 1000) / 10
      : 0;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("trials.title")}</CardTitle>
        <CardDescription>{translate("trials.cohort")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-wrap items-baseline gap-6">
        <Figure label={translate("trials.claimed")} value={String(report.trials.trials)} />
        <Figure label={translate("trials.converted")} value={String(report.trials.converted)} />
        <Figure
          label={translate("trials.rate")}
          value={report.trials.trials > 0 ? `${rate}%` : "—"}
        />
      </CardContent>
    </Card>
  );
}

function Figure({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <p className="font-mono text-[11px] text-subtle-foreground">{label}</p>
      <p className="font-semibold text-2xl tabular-nums">{value}</p>
    </div>
  );
}

function OperationCard({ locale, report }: { locale: string; report: SalesReport }) {
  const translate = useTranslations("admin.reports");
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("operations.title")}</CardTitle>
        <CardDescription>{translate("operations.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        {report.byOperation.length === 0 ? (
          <StateNotice description={translate("empty")} title={translate("noSales")} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("operations.operation")}</TableHead>
                <TableHead>{translate("currency")}</TableHead>
                <TableHead className="text-right">{translate("orders")}</TableHead>
                <TableHead className="text-right">{translate("discount")}</TableHead>
                <TableHead className="text-right">{translate("provider")}</TableHead>
                <TableHead className="text-right">{translate("wallet")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {report.byOperation.map((line) => (
                <TableRow key={`${line.operation}-${line.currency}`}>
                  <TableCell>{translate(`operation.${line.operation}`)}</TableCell>
                  <TableCell>
                    <Badge variant="neutral">{line.currency}</Badge>
                  </TableCell>
                  <TableCell className="text-right tabular-nums">{line.orders}</TableCell>
                  <TableCell className="text-right tabular-nums">
                    {formatMoney(line.discountMinor, line.currency, locale)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {formatMoney(line.paidMinor, line.currency, locale)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {formatMoney(line.walletMinor, line.currency, locale)}
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

function PlanCard({ locale, report }: { locale: string; report: SalesReport }) {
  const translate = useTranslations("admin.reports");
  if (report.byPlan.length === 0) {
    return null;
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("plans.title")}</CardTitle>
        <CardDescription>{translate("plans.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("plans.plan")}</TableHead>
              <TableHead>{translate("plans.period")}</TableHead>
              <TableHead>{translate("currency")}</TableHead>
              <TableHead className="text-right">{translate("orders")}</TableHead>
              <TableHead className="text-right">{translate("plans.gross")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {report.byPlan.map((line) => (
              <TableRow key={`${line.planCode}-${line.planVersion}-${line.currency}`}>
                <TableCell className="font-mono text-xs">
                  {line.planCode}
                  <span className="text-subtle-foreground"> v{line.planVersion}</span>
                </TableCell>
                <TableCell>{translate(`billingPeriod.${line.billingPeriod}`)}</TableCell>
                <TableCell>
                  <Badge variant="neutral">{line.currency}</Badge>
                </TableCell>
                <TableCell className="text-right tabular-nums">{line.orders}</TableCell>
                <TableCell className="text-right tabular-nums">
                  {formatMoney(line.grossMinor, line.currency, locale)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}

function RefundCard({ locale, report }: { locale: string; report: SalesReport }) {
  const translate = useTranslations("admin.reports");
  if (report.refunds.length === 0) {
    return null;
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("refunds.title")}</CardTitle>
        <CardDescription>{translate("refunds.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-wrap gap-6">
        {report.refunds.map((line) => (
          <Figure
            key={line.currency}
            label={`${line.currency} · ${translate("refunds.count", { count: line.refunds })}`}
            value={formatMoney(line.refundedMinor, line.currency, locale)}
          />
        ))}
      </CardContent>
    </Card>
  );
}

function daysAgo(days: number): Date {
  const date = new Date();
  date.setDate(date.getDate() - days);
  return date;
}

/** `YYYY-MM-DDTHH:mm` in local time, which is what DateTimeField reads. */
function localInput(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  return (
    `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}` +
    `T${pad(date.getHours())}:${pad(date.getMinutes())}`
  );
}
