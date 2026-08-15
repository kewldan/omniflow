"use client";

import { ChartFigure, chartAxis, chartColor, chartGrid } from "@omniflow/ui/chart";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts";

import { formatBytes, type TrafficReport } from "@/lib/operations";

/**
 * Node pressure and the heaviest consumers, read from the same live response
 * the tables below them are.
 *
 * Nothing here is stored. Remnawave owns traffic, so these are pictures of one
 * moment rather than of a trend — there is no history to plot and keeping one
 * would be the first step towards Omniflow having an opinion about traffic.
 */

/**
 * How full each limited node is.
 *
 * Only nodes with a limit appear: a node with none cannot be "filling up", and
 * plotting it at zero percent would suggest it has room in the same units as one
 * that genuinely does.
 */
export function NodePressureChart({ report }: { report: TrafficReport }) {
  const translate = useTranslations("admin.traffic");

  const rows = report.nodes
    .filter((node) => node.usedShare !== undefined && node.limitBytes > 0)
    .map((node) => ({
      key: node.name,
      label: node.name,
      share: Math.round((node.usedShare ?? 0) * 1000) / 10,
      usedBytes: node.usedBytes,
    }))
    .sort((left, right) => right.share - left.share)
    .slice(0, 12);

  if (!report.nodesReported || rows.length === 0) {
    return null;
  }

  return (
    <ChartFigure
      description={translate("charts.pressureDescription")}
      empty={translate("charts.pressureEmpty")}
      height={Math.max(180, rows.length * 28)}
      isEmpty={rows.every((row) => row.share === 0)}
      table={
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("nodes.node")}</TableHead>
              <TableHead className="text-right">{translate("nodes.used")}</TableHead>
              <TableHead className="text-right">{translate("charts.share")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <TableRow key={row.key}>
                <TableCell>{row.label}</TableCell>
                <TableCell className="text-right tabular-nums">
                  {formatBytes(row.usedBytes)}
                </TableCell>
                <TableCell className="text-right tabular-nums">{row.share}%</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      }
      title={translate("charts.pressure")}
    >
      <BarChart data={rows} layout="vertical" margin={{ left: 8, right: 16, top: 4 }}>
        <CartesianGrid {...chartGrid} horizontal={false} vertical />
        {/* Fixed to 100 rather than the largest bar: "which node is fullest" and
            "how full is it" are different questions, and an auto-scaled axis
            answers the first while implying the second. */}
        <XAxis
          {...chartAxis}
          domain={[0, 100]}
          tickFormatter={(value: number) => `${value}%`}
          type="number"
        />
        <YAxis {...chartAxis} dataKey="label" type="category" width={130} />
        <Bar dataKey="share" fill={chartColor("chart-1")} maxBarSize={18} radius={[0, 4, 4, 0]} />
      </BarChart>
    </ChartFigure>
  );
}

/** The heaviest users in the window that was scanned, largest first. */
export function ConsumerChart({ report }: { report: TrafficReport }) {
  const translate = useTranslations("admin.traffic");

  const rows = report.consumers
    .slice()
    .sort((left, right) => right.usedBytes - left.usedBytes)
    .slice(0, 10)
    .map((consumer) => ({
      key: String(consumer.remnawaveId),
      label: consumer.label || consumer.username,
      usedBytes: consumer.usedBytes,
    }));

  if (rows.length === 0) {
    return null;
  }

  return (
    <ChartFigure
      description={translate("charts.consumersDescription", {
        scanned: report.scanned,
        total: report.total,
      })}
      empty={translate("charts.consumersEmpty")}
      height={Math.max(180, rows.length * 28)}
      isEmpty={rows.every((row) => row.usedBytes === 0)}
      table={
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("consumers.user")}</TableHead>
              <TableHead className="text-right">{translate("consumers.used")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <TableRow key={row.key}>
                <TableCell className="font-mono text-xs">{row.label}</TableCell>
                <TableCell className="text-right tabular-nums">
                  {formatBytes(row.usedBytes)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      }
      title={translate("charts.consumers")}
    >
      <BarChart data={rows} layout="vertical" margin={{ left: 8, right: 16, top: 4 }}>
        <CartesianGrid {...chartGrid} horizontal={false} vertical />
        <XAxis {...chartAxis} tickFormatter={(value: number) => formatBytes(value)} type="number" />
        <YAxis {...chartAxis} dataKey="label" type="category" width={130} />
        <Bar
          dataKey="usedBytes"
          fill={chartColor("chart-2")}
          maxBarSize={18}
          radius={[0, 4, 4, 0]}
        />
      </BarChart>
    </ChartFigure>
  );
}
