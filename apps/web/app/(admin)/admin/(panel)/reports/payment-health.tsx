"use client";

import { Badge } from "@omniflow/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useLocale, useTranslations } from "next-intl";
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
