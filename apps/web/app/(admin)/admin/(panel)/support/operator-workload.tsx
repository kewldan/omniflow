"use client";

import { ChartFigure, chartAxis, chartColor, chartGrid } from "@omniflow/ui/chart";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts";

import { formatDuration } from "@/lib/operations";

import type { OperatorLoad } from "./types";

/**
 * Replies per operator, and the full table behind them.
 *
 * It lives in its own file so the charting library can be loaded on demand: it
 * is a quarter of a megabyte, this report sits behind a tab, and including it
 * in the support page's first load put that page over the performance budget.
 */
export function OperatorWorkload({ operators }: { operators: OperatorLoad[] }) {
  const translate = useTranslations("admin.support.report");

  return (
    <ChartFigure
      description={translate("figures.replies.definition")}
      empty={translate("noOperators")}
      height={Math.max(140, operators.length * 40)}
      isEmpty={operators.length === 0}
      table={
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("operator")}</TableHead>
              <TableHead>{translate("replies")}</TableHead>
              <TableHead>{translate("openTickets")}</TableHead>
              <TableHead>{translate("resolvedTickets")}</TableHead>
              <TableHead>{translate("medianShort")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {operators.map((operator) => (
              <TableRow key={operator.operatorId}>
                <TableCell>{operator.displayName}</TableCell>
                <TableCell data-numeric>{operator.replies}</TableCell>
                <TableCell data-numeric>{operator.openTickets}</TableCell>
                <TableCell data-numeric>{operator.resolvedTickets}</TableCell>
                <TableCell data-numeric>
                  {operator.medianFirstResponseSeconds > 0
                    ? formatDuration(operator.medianFirstResponseSeconds)
                    : "—"}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      }
      title={translate("workload")}
    >
      {/* Horizontal, because the categories are names: a vertical bar chart
          would either truncate them or tilt them. One series, so no legend —
          the title names what is plotted. */}
      <BarChart data={operators} layout="vertical" margin={{ left: 8, right: 16, top: 4 }}>
        <CartesianGrid {...chartGrid} horizontal={false} vertical />
        <XAxis {...chartAxis} allowDecimals={false} type="number" />
        <YAxis {...chartAxis} dataKey="displayName" type="category" width={140} />
        <Bar dataKey="replies" fill={chartColor("chart-1")} maxBarSize={24} radius={[0, 4, 4, 0]} />
      </BarChart>
    </ChartFigure>
  );
}
