"use client";

import { ChartFigure, chartAxis, chartBarTooltip, chartColor, chartGrid } from "@omniflow/ui/chart";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";
import { Bar, BarChart, CartesianGrid, Tooltip, XAxis, YAxis } from "recharts";

import { CustomerChip, customerTitle, useCustomerLabels } from "@/components/admin/customer-chip";
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
        <Tooltip
          {...chartBarTooltip}
          formatter={(value) => [`${value}%`, translate("charts.share")]}
        />
        <Bar dataKey="share" fill={chartColor("chart-1")} maxBarSize={18} radius={[0, 4, 4, 0]} />
      </BarChart>
    </ChartFigure>
  );
}

/** The heaviest users in the window that was scanned, largest first. */
export function ConsumerChart({ report }: { report: TrafficReport }) {
  const translate = useTranslations("admin.traffic");

  const top = report.consumers
    .slice()
    .sort((left, right) => right.usedBytes - left.usedBytes)
    .slice(0, 10);
  // Named by the customer — their Telegram name or @username — rather than
  // by the plan, which every row shares. A Remnawave user Omniflow did not
  // create keeps its panel username.
  const { data: labels } = useCustomerLabels(top.map((consumer) => consumer.customerId));
  const rows = top.map((consumer) => {
    const label = consumer.customerId ? labels?.get(consumer.customerId) : undefined;
    return {
      avatar: label?.avatar ? `/v1/panel/customers/${consumer.customerId}/avatar` : undefined,
      customerId: consumer.customerId,
      key: String(consumer.remnawaveId),
      label: customerTitle(label) ?? consumer.username,
      plan: consumer.label,
      usedBytes: consumer.usedBytes,
    };
  });

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
                <TableCell>
                  {row.customerId ? (
                    <CustomerChip id={row.customerId} />
                  ) : (
                    <span className="font-mono text-xs">{row.label}</span>
                  )}
                </TableCell>
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
        <YAxis
          {...chartAxis}
          dataKey="key"
          interval={0}
          tick={(props) => (
            <ConsumerTick
              row={rows.find((row) => row.key === String(props.payload?.value))}
              x={Number(props.x)}
              y={Number(props.y)}
            />
          )}
          type="category"
          width={170}
        />
        <Tooltip
          {...chartBarTooltip}
          formatter={(value) => [formatBytes(Number(value)), translate("consumers.used")]}
          labelFormatter={(key) => {
            const row = rows.find((candidate) => candidate.key === key);
            return row?.plan ? `${row.label} · ${row.plan}` : (row?.label ?? key);
          }}
        />
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

/**
 * One consumer on the chart's axis: their Telegram picture in a circle, with
 * initials beneath it for a customer Telegram has no picture of, and the name
 * cut to the axis width.
 */
function ConsumerTick({
  row,
  x,
  y,
}: {
  row?: { avatar?: string; key: string; label: string };
  x: number;
  y: number;
}) {
  if (!row) {
    return null;
  }
  const name = row.label.length > 18 ? `${row.label.slice(0, 17)}…` : row.label;
  const initials = row.label
    .replace(/^@/, "")
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase() ?? "")
    .join("");
  const clip = `consumer-${row.key}`;
  return (
    <g transform={`translate(${x},${y})`}>
      <text
        dominantBaseline="central"
        fill="var(--muted-foreground)"
        fontSize={11}
        textAnchor="end"
        x={-26}
      >
        {name}
      </text>
      <circle cx={-12} cy={0} fill="var(--secondary)" r={9} />
      <text
        dominantBaseline="central"
        fill="var(--secondary-foreground)"
        fontSize={8}
        textAnchor="middle"
        x={-12}
      >
        {initials}
      </text>
      {row.avatar && (
        <>
          <clipPath id={clip}>
            <circle cx={-12} cy={0} r={9} />
          </clipPath>
          <image
            clipPath={`url(#${clip})`}
            height={18}
            href={row.avatar}
            preserveAspectRatio="xMidYMid slice"
            width={18}
            x={-21}
            y={-9}
          />
        </>
      )}
    </g>
  );
}
