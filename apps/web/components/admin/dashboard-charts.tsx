"use client";

import { ChartFigure, chartAxis, chartColor, chartGrid } from "@omniflow/ui/chart";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";
import { Bar, BarChart, CartesianGrid, Cell, Pie, PieChart, XAxis, YAxis } from "recharts";

import type { Metric } from "@/lib/operations";

/**
 * The dashboard's shape charts.
 *
 * The tiles above them answer "how many"; these answer "of what", which is the
 * question a row of four numbers is worst at. Three hundred active customers
 * beside two suspended reads the same as three hundred beside two hundred when
 * both are printed as digits at the same size.
 *
 * Every chart here is a composition — parts that sum to a whole the reader
 * already has — and every one is capped at the three series the palette is
 * validated for. A measure that is a subset of another (stale tickets inside
 * open ones, stuck payments inside those in flight) is deliberately not plotted:
 * drawing it as a slice would assert a whole that does not exist.
 */

/** Finds one metric by key, or nothing when the API did not send it. */
function pick(metrics: Metric[], key: string): Metric | undefined {
  return metrics.find((metric) => metric.key === key);
}

type Slice = { key: string; label: string; value: number; fill: string };

/**
 * A composition as a ring, with the same numbers under it as a table.
 *
 * A ring rather than a pie because the middle is where the total goes, and the
 * total is the figure that makes each slice mean something. A ring rather than a
 * stacked bar because these are shares of one population rather than a quantity
 * accumulating over time.
 */
function Composition({
  description,
  metrics,
  keys,
  title,
  totalLabel,
}: {
  description: string;
  metrics: Metric[];
  keys: string[];
  title: string;
  totalLabel: string;
}) {
  const translate = useTranslations("admin.dashboard");

  const slices: Slice[] = keys
    .map((key, index) => {
      const metric = pick(metrics, key);
      if (!metric) {
        return null;
      }
      return {
        fill: chartColor(`chart-${index + 1}` as "chart-1" | "chart-2" | "chart-3"),
        key,
        label: translate(`metrics.${key}`),
        value: metric.value,
      };
    })
    .filter((slice): slice is Slice => slice !== null);

  const total = slices.reduce((sum, slice) => sum + slice.value, 0);

  return (
    <ChartFigure
      description={description}
      empty={translate("charts.empty")}
      height={200}
      isEmpty={total === 0}
      table={
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("charts.measure")}</TableHead>
              <TableHead className="text-right">{translate("charts.count")}</TableHead>
              <TableHead className="text-right">{translate("charts.share")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {slices.map((slice) => (
              <TableRow key={slice.key}>
                <TableCell className="flex items-center gap-2">
                  {/* The swatch sits beside the label rather than instead of it,
                      so the row reads with colour vision or without. */}
                  <span
                    aria-hidden
                    className="size-2.5 shrink-0 rounded-[3px]"
                    style={{ background: slice.fill }}
                  />
                  {slice.label}
                </TableCell>
                <TableCell className="text-right tabular-nums">
                  {slice.value.toLocaleString()}
                </TableCell>
                <TableCell className="text-right tabular-nums text-muted-foreground">
                  {total === 0 ? "—" : `${Math.round((slice.value / total) * 100)}%`}
                </TableCell>
              </TableRow>
            ))}
            <TableRow>
              <TableCell className="font-medium">{totalLabel}</TableCell>
              <TableCell className="text-right font-medium tabular-nums">
                {total.toLocaleString()}
              </TableCell>
              <TableCell />
            </TableRow>
          </TableBody>
        </Table>
      }
      title={title}
    >
      <PieChart>
        <Pie
          data={slices}
          dataKey="value"
          innerRadius="58%"
          nameKey="label"
          outerRadius="86%"
          paddingAngle={2}
          stroke="var(--background)"
          strokeWidth={2}
        >
          {slices.map((slice) => (
            <Cell fill={slice.fill} key={slice.key} />
          ))}
        </Pie>
      </PieChart>
    </ChartFigure>
  );
}

/** Customers by the state their record is in. */
export function CustomerMix({ metrics }: { metrics: Metric[] }) {
  const translate = useTranslations("admin.dashboard");
  return (
    <Composition
      description={translate("charts.customerMixDescription")}
      keys={["activeCustomers", "suspendedCustomers", "deletedCustomers"]}
      metrics={metrics}
      title={translate("charts.customerMix")}
      totalLabel={translate("charts.total")}
    />
  );
}

/** Entitlements by lifecycle state. Renewals due are excluded: they are a subset
 *  of the active ones, and a slice for them would double-count the population. */
export function SubscriptionMix({ metrics }: { metrics: Metric[] }) {
  const translate = useTranslations("admin.dashboard");
  return (
    <Composition
      description={translate("charts.subscriptionMixDescription")}
      keys={["activeEntitlements", "limitedEntitlements", "lapsedEntitlements"]}
      metrics={metrics}
      title={translate("charts.subscriptionMix")}
      totalLabel={translate("charts.total")}
    />
  );
}

/** Payment intents by outcome. Stuck intents are a subset of those in flight and
 *  are left to their tile rather than drawn as a fourth share. */
export function PaymentMix({ metrics }: { metrics: Metric[] }) {
  const translate = useTranslations("admin.dashboard");
  return (
    <Composition
      description={translate("charts.paymentMixDescription")}
      keys={["paymentsSucceeded", "paymentsInFlight", "paymentsFailed"]}
      metrics={metrics}
      title={translate("charts.paymentMix")}
      totalLabel={translate("charts.total")}
    />
  );
}

/**
 * The operations backlog as one horizontal bar chart.
 *
 * Not a composition: these are unrelated queues that happen to be measured at
 * the same moment, and adding them would produce a number nothing is. One
 * series, one colour, sorted by size — the reading an operator wants is "what is
 * the biggest pile", and a sorted bar answers it before the labels are read.
 *
 * A backlog of zero everywhere renders the empty note rather than a row of bars
 * with no length, which is a picture of nothing that still takes up the space of
 * something.
 */
export function OperationsBacklog({ metrics }: { metrics: Metric[] }) {
  const translate = useTranslations("admin.dashboard");

  const bars = metrics
    // The oldest-event age is a duration, not a count, so it cannot share this
    // axis. It stays a tile.
    .filter((metric) => metric.key !== "outboxOldestAgeSeconds")
    .map((metric) => ({
      key: metric.key,
      label: translate(`metrics.${metric.key}`),
      value: metric.value,
    }))
    .sort((left, right) => right.value - left.value);

  return (
    <ChartFigure
      description={translate("charts.backlogDescription")}
      empty={translate("charts.backlogEmpty")}
      height={Math.max(180, bars.length * 26)}
      isEmpty={bars.every((bar) => bar.value === 0)}
      table={
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("charts.queue")}</TableHead>
              <TableHead className="text-right">{translate("charts.count")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {bars.map((bar) => (
              <TableRow key={bar.key}>
                <TableCell>{bar.label}</TableCell>
                <TableCell className="text-right tabular-nums">
                  {bar.value.toLocaleString()}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      }
      title={translate("charts.backlog")}
    >
      <BarChart data={bars} layout="vertical" margin={{ left: 8, right: 16, top: 4 }}>
        <CartesianGrid {...chartGrid} horizontal={false} vertical />
        <XAxis {...chartAxis} allowDecimals={false} type="number" />
        <YAxis {...chartAxis} dataKey="label" type="category" width={148} />
        <Bar dataKey="value" fill={chartColor("chart-1")} maxBarSize={18} radius={[0, 4, 4, 0]} />
      </BarChart>
    </ChartFigure>
  );
}
