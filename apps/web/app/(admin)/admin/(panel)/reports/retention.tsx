"use client";

import type {
  PanelChurnReasonLine,
  PanelMovementLine,
  PanelRenewalForecast,
} from "@omniflow/api-client/generated";
import { Badge } from "@omniflow/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";

import { StateNotice } from "@/components/admin/state-notice";
import { formatMoney } from "@/lib/operations";

const REASONS = [
  "price",
  "blocked",
  "speed",
  "not_needed",
  "switched",
  "technical",
  "other",
] as const;

/**
 * How recurring revenue moved between the start and the end of the period,
 * customer by customer: who arrived, who came back, who paid more or less, and
 * who left. Net revenue retention above one means growth among existing
 * customers outweighs what was lost.
 */
export function MovementCard({ lines, locale }: { lines: PanelMovementLine[]; locale: string }) {
  const translate = useTranslations("admin.subscriptionReport.movement");
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-6 overflow-x-auto">
        {lines.length === 0 ? (
          <StateNotice description={translate("empty")} title={translate("emptyTitle")} />
        ) : (
          lines.map((line) => {
            const money = (minor: number) => formatMoney(minor, line.currency, locale);
            const steps = [
              { key: "start", minor: line.startMrrMinor, customers: line.startCustomers, sign: "" },
              { key: "new", minor: line.newMrrMinor, customers: line.newCustomers, sign: "+" },
              {
                key: "reactivated",
                minor: line.reactivatedMrrMinor,
                customers: line.reactivatedCustomers,
                sign: "+",
              },
              {
                key: "expansion",
                minor: line.expansionMrrMinor,
                customers: line.expansionCustomers,
                sign: "+",
              },
              {
                key: "contraction",
                minor: line.contractionMrrMinor,
                customers: line.contractionCustomers,
                sign: "−",
              },
              {
                key: "churned",
                minor: line.churnedMrrMinor,
                customers: line.churnedCustomers,
                sign: "−",
              },
              { key: "end", minor: line.endMrrMinor, customers: line.endCustomers, sign: "=" },
            ] as const;
            return (
              <div className="flex flex-col gap-3" key={line.currency}>
                <div className="flex flex-wrap items-center gap-2">
                  <Badge variant="neutral">{line.currency}</Badge>
                  <Badge
                    variant={
                      line.netRevenueRetention === undefined
                        ? "neutral"
                        : line.netRevenueRetention >= 1
                          ? "success"
                          : "warning"
                    }
                  >
                    {translate("nrr", { value: percent(line.netRevenueRetention, locale) })}
                  </Badge>
                  <Badge variant="neutral">
                    {translate("grr", { value: percent(line.grossRevenueRetention, locale) })}
                  </Badge>
                </div>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{translate("step")}</TableHead>
                      <TableHead className="text-right">{translate("customers")}</TableHead>
                      <TableHead className="text-right">{translate("mrr")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {steps.map((step) => (
                      <TableRow key={step.key}>
                        <TableCell
                          className={
                            step.key === "start" || step.key === "end" ? "font-medium" : ""
                          }
                        >
                          {translate(`steps.${step.key}`)}
                        </TableCell>
                        <TableCell className="text-right tabular-nums">{step.customers}</TableCell>
                        <TableCell className="text-right tabular-nums">
                          {step.sign === "" || step.sign === "=" || step.minor === 0
                            ? ""
                            : `${step.sign} `}
                          {money(step.minor)}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            );
          })
        )}
      </CardContent>
    </Card>
  );
}

/**
 * The renewals due in the coming days, counted from today whatever the
 * period, and what they should bring at each plan's own recent renewal rate.
 */
export function ForecastCard({
  forecast,
  locale,
}: {
  forecast: PanelRenewalForecast;
  locale: string;
}) {
  const translate = useTranslations("admin.subscriptionReport.forecast");
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title", { days: forecast.horizonDays })}</CardTitle>
        <CardDescription>{translate("description", { basis: forecast.basisDays })}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-6 overflow-x-auto">
        {forecast.lines.length === 0 ? (
          <StateNotice description={translate("empty")} title={translate("emptyTitle")} />
        ) : (
          <>
            <div className="flex flex-wrap gap-6">
              {forecast.totals.map((total) => (
                <div className="flex flex-col" key={total.currency}>
                  <span className="text-2xl font-semibold tabular-nums">
                    {total.expectedMinor === undefined
                      ? "—"
                      : formatMoney(total.expectedMinor, total.currency, locale)}
                  </span>
                  <span className="text-muted-foreground text-xs">
                    {translate("totalHint", {
                      due: total.due,
                      auto: total.autoRenew,
                      value: formatMoney(total.valueMinor, total.currency, locale),
                    })}
                  </span>
                </div>
              ))}
            </div>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("plan")}</TableHead>
                  <TableHead className="text-right">{translate("due")}</TableHead>
                  <TableHead className="text-right">{translate("auto")}</TableHead>
                  <TableHead className="text-right">{translate("rate")}</TableHead>
                  <TableHead className="text-right">{translate("value")}</TableHead>
                  <TableHead className="text-right">{translate("expected")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {forecast.lines.map((line) => (
                  <TableRow key={`${line.planCode}-${line.billingPeriod}-${line.currency}`}>
                    <TableCell>
                      <span className="font-mono text-xs">{line.planCode}</span>{" "}
                      <span className="text-muted-foreground text-xs">{line.billingPeriod}</span>{" "}
                      <Badge variant="neutral">{line.currency}</Badge>
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{line.due}</TableCell>
                    <TableCell className="text-right tabular-nums">{line.autoRenew}</TableCell>
                    <TableCell className="text-right tabular-nums">
                      {percent(line.renewalRate, locale)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {formatMoney(line.valueMinor, line.currency, locale)}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {line.expectedMinor === undefined
                        ? "—"
                        : formatMoney(line.expectedMinor, line.currency, locale)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </>
        )}
      </CardContent>
    </Card>
  );
}

/**
 * What customers said when they switched renewal off or were told their
 * subscription expired. Answering is optional, so the counts are the
 * customers who chose to say, not every customer who left.
 */
export function ChurnReasonsCard({
  lines,
  locale,
}: {
  lines: PanelChurnReasonLine[];
  locale: string;
}) {
  const translate = useTranslations("admin.subscriptionReport.churnReasons");
  const occasions = ["auto_renew_off", "lapsed"] as const;
  const total = lines.reduce((sum, line) => sum + line.answers, 0);
  const byReason = REASONS.map((reason) => ({
    reason,
    counts: occasions.map(
      (occasion) =>
        lines.find((line) => line.reason === reason && line.occasion === occasion)?.answers ?? 0,
    ),
  }))
    .map((entry) => ({ ...entry, sum: entry.counts[0] + entry.counts[1] }))
    .sort((left, right) => right.sum - left.sum);
  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="overflow-x-auto">
        {total === 0 ? (
          <StateNotice description={translate("empty")} title={translate("emptyTitle")} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("reason")}</TableHead>
                <TableHead className="text-right">
                  {translate("occasions.auto_renew_off")}
                </TableHead>
                <TableHead className="text-right">{translate("occasions.lapsed")}</TableHead>
                <TableHead className="text-right">{translate("share")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {byReason.map((entry) => (
                <TableRow key={entry.reason}>
                  <TableCell>{translate(`reasons.${entry.reason}`)}</TableCell>
                  <TableCell className="text-right tabular-nums">{entry.counts[0]}</TableCell>
                  <TableCell className="text-right tabular-nums">{entry.counts[1]}</TableCell>
                  <TableCell className="text-right tabular-nums">
                    {percent(entry.sum / total, locale)}
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
