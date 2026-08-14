"use client";

import type { ReactElement, ReactNode } from "react";
import { ResponsiveContainer } from "recharts";

import { cn } from "./lib/utils";

/**
 * The chart frame and the series palette.
 *
 * The design system is neutral by construction — zinc surfaces, one foreground
 * accent, and status colours reserved for good/warning/serious/critical. It
 * therefore has no categorical hues, so charts bring their own, and the set
 * below is validated rather than chosen: on this system's own chart surfaces
 * (`#ffffff` light, `#18181b` dark) all three slots sit inside the lightness
 * band, clear the chroma floor, and separate under deuteranopia and tritanopia
 * by ΔE 9.2 light / 9.4 dark against a target of 8, with a normal-vision worst
 * pair of 24.0 / 20.9 against a floor of 15.
 *
 * Three slots, not eight, and that is the ceiling rather than a starting point.
 * The fourth hue in the source order puts yellow beside orange, and that pair
 * fails the separation floors when every pair can appear together. A fourth
 * series folds into "other", or the chart becomes small multiples.
 *
 * Aqua sits at 2.82:1 on the white surface, below the 3:1 mark. That is a
 * documented relief rather than a failure, and it obliges every chart here to
 * carry the values as text — a direct label or the table beside it — so nothing
 * is read from colour alone. `ChartFigure` is built so that obligation is met
 * by default rather than remembered.
 */
export const CHART_SERIES = ["chart-1", "chart-2", "chart-3"] as const;

export type ChartSeriesSlot = (typeof CHART_SERIES)[number];

/** Resolves a slot to the CSS variable the theme defines for both modes. */
export function chartColor(slot: ChartSeriesSlot): string {
  return `var(--${slot})`;
}

export type ChartFigureProps = {
  /** Names what is plotted. A single-series chart needs no legend because of it. */
  title: ReactNode;
  /** What was counted, in the reader's terms. */
  description?: string;
  /**
   * The same numbers as text.
   *
   * It is required rather than optional. One series colour is below the
   * contrast floor on the light surface, and a chart is not a substitute for
   * the figures anyway — an operator who needs the exact number should never
   * have to read it off an axis.
   */
  table: ReactNode;
  /** Shown instead of the plot when there is nothing to plot. */
  empty?: ReactNode;
  isEmpty?: boolean;
  height?: number;
  className?: string;
  children: ReactElement;
};

/**
 * One chart, its heading, and the figures behind it.
 *
 * The table is not a fallback for a broken chart — it is the accessible reading
 * of the same data, always rendered, collapsed under a disclosure so it costs
 * nothing to ignore and nothing to find.
 */
export function ChartFigure({
  children,
  className,
  description,
  empty,
  height = 220,
  isEmpty,
  table,
  title,
}: ChartFigureProps) {
  return (
    <figure className={cn("flex flex-col gap-2", className)}>
      <figcaption className="flex flex-col gap-0.5">
        <span className="flex items-center gap-2 font-medium text-sm">{title}</span>
        {description ? <span className="text-muted-foreground text-xs">{description}</span> : null}
      </figcaption>

      {isEmpty ? (
        <p className="py-8 text-center text-muted-foreground text-sm">{empty}</p>
      ) : (
        <>
          {/* aria-hidden: the plot is decorative beside the table, which carries
              the same numbers in a form a screen reader can read in order. */}
          <div aria-hidden className="w-full" style={{ height }}>
            <ResponsiveContainer height="100%" width="100%">
              {children}
            </ResponsiveContainer>
          </div>
          {table}
        </>
      )}
    </figure>
  );
}

/** Axis and grid styling shared by every chart, so none of them drifts. */
export const chartAxis = {
  axisLine: false,
  tickLine: false,
  tick: { fill: "var(--muted-foreground)", fontSize: 11 },
} as const;

export const chartGrid = {
  stroke: "var(--border)",
  strokeDasharray: "3 3",
  vertical: false,
} as const;
