"use client";

import { Badge } from "@omniflow/ui/badge";
import { ChartFigure, chartAxis, chartColor, chartGrid } from "@omniflow/ui/chart";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useLocale, useTranslations } from "next-intl";
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts";

import { formatMoney, type RevenueLine } from "@/lib/operations";

/**
 * Where the money came from, per currency.
 *
 * Grouped rather than stacked, and that is the entire point. Provider money,
 * wallet spend, and refunds are three figures the domain says must never be
 * added: balance was already counted as revenue when it was funded, so stacking
 * it on top of provider money counts it twice, and a stack is a picture of a
 * total. Side by side, the three read as three.
 *
 * One chart per currency, because two currencies on one axis would be comparing
 * numbers that are not comparable — a hundred of one is not a hundred of the
 * other, and a shared scale asserts that it is.
 */
export function RevenueChart({ lines }: { lines: RevenueLine[] }) {
  const translate = useTranslations("admin.dashboard.revenue");
  const locale = useLocale();

  if (lines.length === 0) {
    return null;
  }

  return (
    <div className="grid gap-6 lg:grid-cols-2">
      {lines.map((line) => {
        const data = [
          { fill: chartColor("chart-1"), key: "paid", value: line.paidMinor },
          { fill: chartColor("chart-2"), key: "wallet", value: line.walletMinor },
          { fill: chartColor("chart-3"), key: "refunded", value: line.refundedMinor },
        ].map((entry) => ({ ...entry, label: translate(entry.key) }));

        return (
          <ChartFigure
            description={translate("definition", { orders: line.orderCount })}
            isEmpty={data.every((entry) => entry.value === 0)}
            empty={translate("none")}
            key={line.currency}
            table={
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{translate("measure")}</TableHead>
                    <TableHead className="text-right">{translate("amount")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.map((entry) => (
                    <TableRow key={entry.key}>
                      <TableCell className="flex items-center gap-2">
                        {/* The swatch carries identity beside the label rather
                            than instead of it, so the row reads without colour. */}
                        <span
                          aria-hidden
                          className="size-2.5 shrink-0 rounded-[3px]"
                          style={{ background: entry.fill }}
                        />
                        {entry.label}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">
                        {formatMoney(entry.value, line.currency, locale)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            }
            title={
              <>
                {translate("title")} <Badge variant="neutral">{line.currency}</Badge>
              </>
            }
          >
            <BarChart data={data} margin={{ left: 8, right: 8, top: 8 }}>
              <CartesianGrid {...chartGrid} />
              <XAxis {...chartAxis} dataKey="label" />
              <YAxis
                {...chartAxis}
                tickFormatter={(value: number) =>
                  formatMoney(value, line.currency, locale, { compact: true })
                }
                width={72}
              />
              <Bar
                // 4px rounded ends anchored to the baseline, and a bar thin
                // enough that three of them are a comparison rather than a
                // block of colour.
                dataKey="value"
                maxBarSize={56}
                radius={[4, 4, 0, 0]}
              />
            </BarChart>
          </ChartFigure>
        );
      })}
    </div>
  );
}
