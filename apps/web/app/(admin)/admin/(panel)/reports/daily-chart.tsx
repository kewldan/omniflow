"use client";

import { Badge } from "@omniflow/ui/badge";
import {
  ChartFigure,
  chartAxis,
  chartBarTooltip,
  chartColor,
  chartGrid,
  chartTooltip,
} from "@omniflow/ui/chart";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";
import { Area, AreaChart, Bar, BarChart, CartesianGrid, Tooltip, XAxis, YAxis } from "recharts";

import { formatMoney, type SalesReport } from "@/lib/operations";

/**
 * Provider money by day, one chart per currency.
 *
 * Two currencies on one axis would assert that a hundred of one is a hundred of
 * the other, which is the same reason the dashboard's revenue chart is split.
 */
export function DailyChart({ locale, report }: { locale: string; report: SalesReport }) {
  const translate = useTranslations("admin.reports");
  const currencies = [...new Set(report.byDay.map((line) => line.currency))].sort();

  if (currencies.length === 0) {
    return null;
  }

  return (
    <div className="grid gap-6 lg:grid-cols-2">
      {currencies.map((currency) => {
        const days = report.byDay.filter((line) => line.currency === currency);
        return (
          <ChartFigure
            description={translate("daily.description")}
            empty={translate("empty")}
            isEmpty={days.every((day) => day.paidMinor === 0)}
            key={currency}
            table={
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{translate("daily.day")}</TableHead>
                    <TableHead className="text-right">{translate("orders")}</TableHead>
                    <TableHead className="text-right">{translate("provider")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {days.map((day) => (
                    <TableRow key={day.day}>
                      <TableCell className="font-mono text-xs">{day.day}</TableCell>
                      <TableCell className="text-right tabular-nums">{day.orders}</TableCell>
                      <TableCell className="text-right tabular-nums">
                        {formatMoney(day.paidMinor, currency, locale)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            }
            title={
              <>
                {translate("daily.title")} <Badge variant="neutral">{currency}</Badge>
              </>
            }
          >
            <BarChart data={days.map((day) => ({ ...day, fill: chartColor("chart-1") }))}>
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
                {...chartBarTooltip}
                formatter={(value) => [
                  formatMoney(Number(value), currency, locale),
                  translate("provider"),
                ]}
              />
              <Bar dataKey="paidMinor" radius={4} />
            </BarChart>
          </ChartFigure>
        );
      })}
    </div>
  );
}

/**
 * How many orders were placed each day, regardless of what they were worth.
 *
 * Separate from the money chart rather than a second axis on it. One axis per
 * chart is the rule the rest of this panel keeps, and the two questions are
 * genuinely different: a quiet week of large orders and a busy week of small
 * ones look identical in revenue and opposite here.
 *
 * Days are summed across currencies, because an order is an order whatever it
 * settled in — which is exactly the reason the money below cannot be.
 */
export function OrdersChart({ report }: { report: SalesReport }) {
  const translate = useTranslations("admin.reports");

  const byDay = new Map<string, number>();
  for (const line of report.byDay) {
    byDay.set(line.day, (byDay.get(line.day) ?? 0) + line.orders);
  }
  const days = [...byDay.entries()]
    .map(([day, orders]) => ({ day, orders }))
    .sort((left, right) => left.day.localeCompare(right.day));

  if (days.length === 0) {
    return null;
  }
  // The chart walks every day of the span, a day without orders at zero: a
  // day missing from the axis would be drawn as whatever its neighbours were.
  const plottedDays = everyDay(days);

  return (
    <ChartFigure
      description={translate("ordersDaily.description")}
      empty={translate("empty")}
      isEmpty={days.every((day) => day.orders === 0)}
      table={
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("daily.day")}</TableHead>
              <TableHead className="text-right">{translate("orders")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {days.map((day) => (
              <TableRow key={day.day}>
                <TableCell className="font-mono text-xs">{day.day}</TableCell>
                <TableCell className="text-right tabular-nums">{day.orders}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      }
      title={translate("ordersDaily.title")}
    >
      <AreaChart data={plottedDays} margin={{ left: 8, right: 8, top: 8 }}>
        <defs>
          <linearGradient id="orders-fill" x1="0" x2="0" y1="0" y2="1">
            <stop offset="0%" stopColor={chartColor("chart-1")} stopOpacity={0.35} />
            <stop offset="100%" stopColor={chartColor("chart-1")} stopOpacity={0.02} />
          </linearGradient>
        </defs>
        <CartesianGrid {...chartGrid} />
        <XAxis {...chartAxis} dataKey="day" />
        <YAxis {...chartAxis} allowDecimals={false} width={40} />
        <Tooltip {...chartTooltip} formatter={(value) => [value, translate("orders")]} />
        <Area
          activeDot={{ r: 4, stroke: "var(--background)", strokeWidth: 2 }}
          dataKey="orders"
          fill="url(#orders-fill)"
          stroke={chartColor("chart-1")}
          strokeWidth={2}
          type="monotone"
        />
      </AreaChart>
    </ChartFigure>
  );
}

/**
 * Which plans the period's money came from.
 *
 * Sorted by revenue and capped, because a catalogue of thirty plans produces a
 * chart of thirty labels nobody reads. What is cut is still in the table below,
 * which is the whole reason the table is not optional.
 */
export function PlanChart({ locale, report }: { locale: string; report: SalesReport }) {
  const translate = useTranslations("admin.reports");
  const currencies = [...new Set(report.byPlan.map((line) => line.currency))].sort();

  if (currencies.length === 0) {
    return null;
  }

  return (
    <div className="grid gap-6 lg:grid-cols-2">
      {currencies.map((currency) => {
        const plans = report.byPlan
          .filter((line) => line.currency === currency)
          .sort((left, right) => right.grossMinor - left.grossMinor);
        const plotted = plans.slice(0, 8).map((line) => ({
          ...line,
          fill: chartColor("chart-1"),
          label: `${line.planCode} v${line.planVersion}`,
        }));

        return (
          <ChartFigure
            description={translate("planChart.description")}
            empty={translate("empty")}
            height={Math.max(180, plotted.length * 30)}
            isEmpty={plans.every((line) => line.grossMinor === 0)}
            key={currency}
            table={
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{translate("plan")}</TableHead>
                    <TableHead className="text-right">{translate("orders")}</TableHead>
                    <TableHead className="text-right">{translate("gross")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {plans.map((line) => (
                    <TableRow key={`${line.planCode}-${line.planVersion}`}>
                      <TableCell className="font-mono text-xs">
                        {line.planCode} v{line.planVersion}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">{line.orders}</TableCell>
                      <TableCell className="text-right tabular-nums">
                        {formatMoney(line.grossMinor, currency, locale)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            }
            title={
              <>
                {translate("planChart.title")} <Badge variant="neutral">{currency}</Badge>
              </>
            }
          >
            <BarChart data={plotted} layout="vertical" margin={{ left: 8, right: 16, top: 4 }}>
              <CartesianGrid {...chartGrid} horizontal={false} vertical />
              <XAxis
                {...chartAxis}
                tickFormatter={(value: number) =>
                  formatMoney(value, currency, locale, { compact: true })
                }
                type="number"
              />
              <YAxis {...chartAxis} dataKey="label" type="category" width={120} />
              <Tooltip
                {...chartBarTooltip}
                formatter={(value) => [
                  formatMoney(Number(value), currency, locale),
                  translate("gross"),
                ]}
              />
              <Bar dataKey="grossMinor" maxBarSize={20} radius={[0, 4, 4, 0]} />
            </BarChart>
          </ChartFigure>
        );
      })}
    </div>
  );
}

/** Every calendar day from the first to the last, a missing one at zero. */
function everyDay(days: { day: string; orders: number }[]): { day: string; orders: number }[] {
  const byDay = new Map(days.map((day) => [day.day, day.orders]));
  const first = new Date(`${days[0]?.day}T00:00:00Z`);
  const last = new Date(`${days[days.length - 1]?.day}T00:00:00Z`);
  if (Number.isNaN(first.getTime()) || Number.isNaN(last.getTime())) {
    return days;
  }
  const filled: { day: string; orders: number }[] = [];
  for (
    let at = first;
    at <= last && filled.length < 800;
    at = new Date(at.getTime() + 86_400_000)
  ) {
    const day = at.toISOString().slice(0, 10);
    filled.push({ day, orders: byDay.get(day) ?? 0 });
  }
  return filled;
}
