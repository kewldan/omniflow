import type { DeltaPolarity } from "@omniflow/ui/delta-badge";

/**
 * Which way is good for each dashboard metric.
 *
 * Presentation rather than policy, like the tile links: it decides the colour
 * of a chip, not what anyone may do. A metric with no entry is neutral, which
 * is the honest default — a tile that turned green or red on a guess would be
 * asserting a verdict nobody made.
 */
export const METRIC_POLARITY: Record<string, DeltaPolarity> = {
  newCustomers: "higher-is-better",
  paymentsFailed: "lower-is-better",
  paymentsSucceeded: "higher-is-better",
  paidMinor: "higher-is-better",
};

export function metricPolarity(key: string): DeltaPolarity {
  return METRIC_POLARITY[key] ?? "neutral";
}

/**
 * The change against the previous window, as a fraction (0.12 for +12%).
 *
 * Null when the previous window was empty: growth from nothing has no
 * percentage, and "+∞%" or "+100%" would both be inventions. The caller shows
 * the absolute change instead.
 */
export function relativeChange(value: number, previous: number): number | null {
  if (previous === 0) {
    return null;
  }
  return (value - previous) / previous;
}

/** How many of the newest buckets count as "recent" against the rest. */
export const kRecentDays = 7;

/**
 * A recent stretch has to differ from the usual pace by at least this much
 * before it earns a note. Below it, the difference is a week's noise.
 */
export const kNotableShift = 0.25;

export type TrendNote =
  /** The newest bucket is the highest in the series. */
  | { kind: "peak" }
  /** Nothing in the recent stretch, after something before it. */
  | { kind: "quiet"; days: number }
  /** The recent daily average against the rest of the series, as a fraction. */
  | { kind: "above" | "below"; days: number; shift: number }
  /** Upcoming: how many fall due soon, and the busiest bucket. */
  | { kind: "upcoming"; days: number; soon: number; busiestIndex: number; busiest: number };

/**
 * The one sentence worth saying about a series, or nothing.
 *
 * It reads the shape the tile's number cannot show. At most one note per tile,
 * picked in order of how much it should change what the operator does next: a
 * record first, then a measure that has gone silent, then a pace shift. A
 * series with nothing notable gets no note rather than a filler one.
 */
export function trendNote(
  values: readonly number[],
  kind: "history" | "upcoming",
): TrendNote | null {
  if (values.length === 0) {
    return null;
  }

  if (kind === "upcoming") {
    const soon = sum(values.slice(0, kRecentDays));
    let busiestIndex = 0;
    values.forEach((value, index) => {
      if (value > (values[busiestIndex] ?? 0)) {
        busiestIndex = index;
      }
    });
    const busiest = values[busiestIndex] ?? 0;
    if (busiest === 0) {
      return null;
    }
    return { busiest, busiestIndex, days: kRecentDays, kind: "upcoming", soon };
  }

  // Needs enough history for "recent" to have something to be compared with.
  if (values.length <= kRecentDays) {
    return null;
  }
  const newest = values[values.length - 1] ?? 0;
  const older = values.slice(0, -1);
  if (newest > 0 && newest > Math.max(...older)) {
    return { kind: "peak" };
  }

  const recent = values.slice(-kRecentDays);
  const before = values.slice(0, -kRecentDays);
  const recentAverage = sum(recent) / recent.length;
  const usualAverage = sum(before) / before.length;
  if (recentAverage === 0 && usualAverage > 0) {
    return { days: kRecentDays, kind: "quiet" };
  }
  if (usualAverage === 0) {
    return null;
  }
  const shift = (recentAverage - usualAverage) / usualAverage;
  if (Math.abs(shift) < kNotableShift) {
    return null;
  }
  return { days: kRecentDays, kind: shift > 0 ? "above" : "below", shift: Math.abs(shift) };
}

/**
 * The direction a note implies, so it can take the same tone as the chip.
 * A peak and an above-usual stretch are rises; quiet and below are falls.
 */
export function noteDelta(note: TrendNote): number {
  switch (note.kind) {
    case "peak":
    case "above":
      return 1;
    case "quiet":
    case "below":
      return -1;
    default:
      return 0;
  }
}

function sum(values: readonly number[]): number {
  return values.reduce((total, value) => total + value, 0);
}
