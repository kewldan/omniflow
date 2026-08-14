"use client";

import { Badge } from "@omniflow/ui/badge";
import { ChartFigure, chartAxis, chartColor, chartGrid } from "@omniflow/ui/chart";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts";

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
              <YAxis {...chartAxis} />
              <Bar dataKey="paidMinor" radius={4} />
            </BarChart>
          </ChartFigure>
        );
      })}
    </div>
  );
}
