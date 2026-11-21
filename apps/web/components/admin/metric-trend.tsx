"use client";

import { DeltaBadge, type DeltaPolarity, deltaTone } from "@omniflow/ui/delta-badge";
import { cn } from "@omniflow/ui/lib/utils";
import { Sparkline } from "@omniflow/ui/sparkline";
import { useFormatter, useTranslations } from "next-intl";

import { noteDelta, relativeChange, trendNote } from "@/lib/metric-trends";
import type { SeriesPoint } from "@/lib/operations";

/**
 * The change against the previous window as a chip.
 *
 * A percentage when the previous window had something to be a percentage of,
 * the absolute change when it did not: growth from nothing has no percentage,
 * and "+100%" for three sign-ups after none would be a number nobody measured.
 */
export function MetricDelta({
  format: formatValue,
  polarity,
  previous,
  value,
  windowDays,
}: {
  value: number;
  previous: number;
  polarity: DeltaPolarity;
  windowDays: number;
  /** Formats an absolute change, for measures that are not plain counts. */
  format?: (value: number) => string;
}) {
  const translate = useTranslations("admin.dashboard.trend");
  const format = useFormatter();
  const delta = value - previous;
  const description = translate("versusPrevious", { days: windowDays });

  if (delta === 0) {
    return (
      <DeltaBadge delta={0} description={description} polarity={polarity}>
        {translate("unchanged")}
      </DeltaBadge>
    );
  }

  const change = relativeChange(value, previous);
  const text =
    change === null
      ? `+${formatValue ? formatValue(delta) : format.number(delta)}`
      : format.number(Math.abs(change), {
          maximumFractionDigits: Math.abs(change) < 0.1 ? 1 : 0,
          style: "percent",
        });

  return (
    <DeltaBadge delta={delta} description={description} polarity={polarity}>
      {text}
    </DeltaBadge>
  );
}

/**
 * The series as a sparkline, with the same figures in words for a screen
 * reader. History is a line, because it happened continuously; what falls due
 * is bars, one per day, because it is a schedule.
 */
export function MetricSparkline({
  className,
  format: formatValue,
  kind,
  series,
}: {
  series: SeriesPoint[];
  kind: "history" | "upcoming";
  format?: (value: number) => string;
  className?: string;
}) {
  const translate = useTranslations("admin.dashboard.trend");
  const format = useFormatter();
  const values = series.map((point) => point.value);
  if (values.length === 0) {
    return null;
  }
  const show = (value: number) => (formatValue ? formatValue(value) : format.number(value));
  const label =
    kind === "upcoming"
      ? translate("upcomingLabel", {
          days: values.length,
          first: show(values[0] ?? 0),
          max: show(Math.max(...values)),
        })
      : translate("historyLabel", {
          days: values.length,
          last: show(values[values.length - 1] ?? 0),
          max: show(Math.max(...values)),
          min: show(Math.min(...values)),
        });

  return (
    <Sparkline
      className={className}
      label={label}
      values={values}
      variant={kind === "upcoming" ? "bars" : "line"}
    />
  );
}

const toneText = {
  danger: "text-destructive-tint-foreground",
  neutral: "text-muted-foreground",
  success: "text-success-tint-foreground",
} as const;

/**
 * The one thing about the series' shape worth saying, or nothing.
 *
 * It takes the chip's tone so the two never disagree: a failure count running
 * above its usual pace reads in the danger tone, a sales count doing the same
 * in the success tone, and a neutral measure stays muted either way.
 */
export function MetricTrendNote({
  className,
  kind,
  polarity,
  series,
}: {
  series: SeriesPoint[];
  kind: "history" | "upcoming";
  polarity: DeltaPolarity;
  className?: string;
}) {
  const translate = useTranslations("admin.dashboard.trend");
  const format = useFormatter();
  const note = trendNote(
    series.map((point) => point.value),
    kind,
  );
  if (!note) {
    return null;
  }

  let text: string;
  switch (note.kind) {
    case "peak":
      text = translate("peak");
      break;
    case "quiet":
      text = translate("quiet", { days: note.days });
      break;
    case "above":
    case "below":
      text = translate(note.kind, {
        days: note.days,
        shift: format.number(note.shift, { maximumFractionDigits: 0, style: "percent" }),
      });
      break;
    case "upcoming": {
      const start = series[note.busiestIndex]?.start;
      text = translate("upcoming", {
        busiest: note.busiest,
        date: start ? format.dateTime(new Date(start), { day: "numeric", month: "short" }) : "",
        days: note.days,
        soon: note.soon,
      });
      break;
    }
  }

  return (
    <p className={cn("text-xs", toneText[deltaTone(noteDelta(note), polarity)], className)}>
      {text}
    </p>
  );
}
