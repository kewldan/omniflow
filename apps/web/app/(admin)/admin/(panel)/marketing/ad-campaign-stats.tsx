"use client";

import { Button } from "@omniflow/ui/button";
import { ChartFigure, chartAxis, chartBarTooltip, chartColor, chartGrid } from "@omniflow/ui/chart";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import { Bar, BarChart, CartesianGrid, Tooltip, XAxis, YAxis } from "recharts";
import useSWR from "swr";
import { type ApiError, fetcher } from "@/lib/api";
import { formatMoney } from "@/lib/operations";

type CampaignDay = {
  day: string;
  registrations: number;
  trials: number;
  firstPayments: number;
  revenueMinor: number;
};

type CampaignStats = {
  campaign: {
    id: string;
    name: string;
    currency: string;
    registrations: number;
    trials: number;
    paying: number;
    revenueMinor: number;
  };
  trialPercent: number;
  payingPercent: number;
  trialToPaidPercent: number;
  revenuePerRegistrationMinor: number;
  days: CampaignDay[];
};

const PERIODS = ["7", "30", "90", "365"] as const;

/**
 * One campaign's report: what its sign-ups went on to do, as totals,
 * conversions, and a chart by day. People and money are on separate charts
 * because one axis cannot honestly carry both.
 */
export function AdCampaignStatsPanel({
  campaignId,
  onClose,
}: {
  campaignId: string;
  onClose: () => void;
}) {
  const translate = useTranslations("admin.marketing.adCampaigns.stats");
  const locale = useLocale();
  const periodId = useId();
  const [days, setDays] = useState<string>("30");
  const { data, error, isLoading } = useSWR<CampaignStats, ApiError>(
    `/v1/panel/marketing/ad-campaigns/${campaignId}/stats?days=${days}`,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-64 w-full" />;
  }
  if (error || !data) {
    return <p className="text-destructive text-sm">{translate("error")}</p>;
  }
  const currency = data.campaign.currency;
  const money = (value: number) => formatMoney(value, currency, locale);
  const tiles = [
    { label: translate("registrations"), value: String(data.campaign.registrations) },
    {
      label: translate("trials"),
      value: `${data.campaign.trials} · ${data.trialPercent}%`,
    },
    {
      label: translate("paying"),
      value: `${data.campaign.paying} · ${data.payingPercent}%`,
    },
    { label: translate("trialToPaid"), value: `${data.trialToPaidPercent}%` },
    { label: translate("revenue"), value: money(data.campaign.revenueMinor) },
    { label: translate("perRegistration"), value: money(data.revenuePerRegistrationMinor) },
  ];
  const people = data.days.map((day) => ({
    ...day,
    label: day.day.slice(5),
  }));

  return (
    <section
      aria-label={translate("title", { name: data.campaign.name })}
      className="flex flex-col gap-4 rounded-lg border bg-well p-4"
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="font-medium">{translate("title", { name: data.campaign.name })}</div>
        <div className="flex items-center gap-2">
          <label className="sr-only" htmlFor={periodId}>
            {translate("period")}
          </label>
          <Select onValueChange={setDays} value={days}>
            <SelectTrigger className="w-36" id={periodId}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PERIODS.map((period) => (
                <SelectItem key={period} value={period}>
                  {translate("days", { count: Number(period) })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button onClick={onClose} size="sm" variant="ghost">
            {translate("close")}
          </Button>
        </div>
      </div>

      <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        {tiles.map((tile) => (
          <div className="rounded-md border bg-well p-3" key={tile.label}>
            <dt className="text-muted-foreground text-xs">{tile.label}</dt>
            <dd className="font-medium tabular-nums">{tile.value}</dd>
          </div>
        ))}
      </dl>

      <div className="grid gap-6 lg:grid-cols-2">
        <ChartFigure
          description={translate("peopleDescription")}
          empty={translate("empty")}
          isEmpty={data.days.every(
            (day) => day.registrations === 0 && day.trials === 0 && day.firstPayments === 0,
          )}
          table={
            <details className="text-sm">
              <summary className="cursor-pointer text-muted-foreground text-xs">
                {translate("table")}
              </summary>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{translate("day")}</TableHead>
                    <TableHead className="text-right">{translate("registrations")}</TableHead>
                    <TableHead className="text-right">{translate("trials")}</TableHead>
                    <TableHead className="text-right">{translate("firstPayments")}</TableHead>
                    <TableHead className="text-right">{translate("revenue")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.days.map((day) => (
                    <TableRow key={day.day}>
                      <TableCell className="font-mono text-xs">{day.day}</TableCell>
                      <TableCell className="text-right tabular-nums">{day.registrations}</TableCell>
                      <TableCell className="text-right tabular-nums">{day.trials}</TableCell>
                      <TableCell className="text-right tabular-nums">{day.firstPayments}</TableCell>
                      <TableCell className="text-right tabular-nums">
                        {money(day.revenueMinor)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </details>
          }
          title={translate("peopleTitle")}
        >
          <BarChart data={people}>
            <CartesianGrid {...chartGrid} />
            <XAxis {...chartAxis} dataKey="label" />
            <YAxis {...chartAxis} allowDecimals={false} width={40} />
            <Tooltip {...chartBarTooltip} />
            <Bar
              dataKey="registrations"
              fill={chartColor("chart-1")}
              name={translate("registrations")}
              radius={3}
            />
            <Bar
              dataKey="trials"
              fill={chartColor("chart-2")}
              name={translate("trials")}
              radius={3}
            />
            <Bar
              dataKey="firstPayments"
              fill={chartColor("chart-3")}
              name={translate("firstPayments")}
              radius={3}
            />
          </BarChart>
        </ChartFigure>
        <ChartFigure
          description={translate("revenueDescription", { currency })}
          empty={translate("empty")}
          isEmpty={data.days.every((day) => day.revenueMinor === 0)}
          table={
            <details className="text-sm">
              <summary className="cursor-pointer text-muted-foreground text-xs">
                {translate("table")}
              </summary>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{translate("day")}</TableHead>
                    <TableHead className="text-right">{translate("revenue")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.days.map((day) => (
                    <TableRow key={day.day}>
                      <TableCell className="font-mono text-xs">{day.day}</TableCell>
                      <TableCell className="text-right tabular-nums">
                        {money(day.revenueMinor)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </details>
          }
          title={translate("revenueTitle")}
        >
          <BarChart data={people}>
            <CartesianGrid {...chartGrid} />
            <XAxis {...chartAxis} dataKey="label" />
            <YAxis
              {...chartAxis}
              tickFormatter={(value: number) =>
                formatMoney(value, currency, locale, { compact: true })
              }
              width={72}
            />
            <Tooltip
              {...chartBarTooltip}
              formatter={(value) => [money(Number(value)), translate("revenue")]}
            />
            <Bar dataKey="revenueMinor" fill={chartColor("chart-1")} radius={3} />
          </BarChart>
        </ChartFigure>
      </div>
      <p className="text-muted-foreground text-xs">{translate("legend")}</p>
    </section>
  );
}
