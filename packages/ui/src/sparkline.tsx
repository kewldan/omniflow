import type { ComponentProps } from "react";

import type { ChartSeriesSlot } from "./chart";
import { cn } from "./lib/utils";

export type SparklineProps = Omit<ComponentProps<"div">, "children"> & {
  /** The values in reading order: oldest first for history, soonest first for a forecast. */
  values: readonly number[];
  /**
   * `line` for a measure over time that has already happened, `bars` for one
   * that is scheduled. A line implies continuity between the points; a run of
   * separate days on which something falls due has none.
   */
  variant?: "line" | "bars";
  /** One of the chart series slots, so a sparkline never brings its own hue. */
  slot?: ChartSeriesSlot;
  height?: number;
  /**
   * The same series in words, for a screen reader. Without one the sparkline is
   * hidden from assistive technology, which is right only when the figures it
   * draws are already written beside it.
   */
  label?: string;
};

/** Vertical padding inside the drawing, so the stroke and dot are never clipped. */
const kInset = 3;

/**
 * A small chart with no axes, meant to sit inside a tile under its number.
 *
 * It is plain SVG rather than a Recharts chart on purpose. The tiles render
 * with the first paint of the page, while the charting library is loaded after
 * it to keep first-load JavaScript inside the budget, and a tile that waited
 * for a quarter of a megabyte to draw thirty points would arrive last instead
 * of first. It also renders on the server, because it needs no measurement:
 * the drawing is in its own coordinate space and stretched to the box.
 *
 * The stretch is why the line uses a non-scaling stroke and the end-of-series
 * dot is an HTML element positioned over the drawing rather than an SVG circle
 * — a circle stretched with the box would become an ellipse.
 */
export function Sparkline({
  className,
  height = 32,
  label,
  slot = "chart-1",
  style,
  values,
  variant = "line",
  ...props
}: SparklineProps) {
  const color = `var(--${slot})`;
  const count = values.length;
  const max = Math.max(0, ...values);
  const min = variant === "bars" ? 0 : Math.min(0, ...values);
  const span = max - min || 1;
  const inner = height - kInset * 2;
  // y measured down from the top of the drawing, which is `height` tall.
  const y = (value: number) => kInset + inner - ((value - min) / span) * inner;

  const accessibility = label ? { "aria-label": label, role: "img" } : { "aria-hidden": true };

  if (count === 0) {
    return null;
  }

  return (
    <div
      className={cn("relative w-full", className)}
      data-slot="sparkline"
      style={{ height, ...style }}
      {...accessibility}
      {...props}
    >
      <svg
        aria-hidden
        className="block size-full overflow-visible"
        focusable="false"
        preserveAspectRatio="none"
        viewBox={`0 0 ${variant === "bars" ? count : Math.max(count - 1, 1)} ${height}`}
      >
        {variant === "bars" ? (
          values.map((value, index) => {
            const top = value > 0 ? Math.min(y(value), height - kInset - 1) : height - kInset;
            return (
              <rect
                // Bars are positional: the index is the day, and the list never
                // reorders.
                // biome-ignore lint/suspicious/noArrayIndexKey: positional series
                key={index}
                fill={value > 0 ? color : "var(--border)"}
                height={value > 0 ? height - kInset - top : 1}
                width={0.7}
                x={index + 0.15}
                y={value > 0 ? top : height - kInset - 1}
              />
            );
          })
        ) : (
          <>
            <polygon
              fill={color}
              fillOpacity={0.1}
              points={`0,${height} ${points(values, y)} ${count - 1},${height}`}
            />
            <polyline
              fill="none"
              points={points(values, y)}
              stroke={color}
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={1.5}
              vectorEffect="non-scaling-stroke"
            />
          </>
        )}
      </svg>
      {variant === "line" ? (
        <span
          aria-hidden
          className="absolute size-1.5 -translate-x-1/2 -translate-y-1/2 rounded-full ring-2 ring-card"
          style={{
            background: color,
            left: count > 1 ? "100%" : "50%",
            top: `${(y(values[count - 1] ?? 0) / height) * 100}%`,
          }}
        />
      ) : null}
    </div>
  );
}

function points(values: readonly number[], y: (value: number) => number): string {
  if (values.length === 1) {
    return `0,${y(values[0] ?? 0)} 1,${y(values[0] ?? 0)}`;
  }
  return values.map((value, index) => `${index},${y(value)}`).join(" ");
}
