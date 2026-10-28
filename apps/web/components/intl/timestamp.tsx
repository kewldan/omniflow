"use client";

import { useFormatter, useNow } from "next-intl";

/** Within this distance of now, a moment reads better as "2 hours ago". */
const RELATIVE_WINDOW_MS = 7 * 86_400_000;

/**
 * One moment, written the one way both panels write moments.
 *
 * - `auto` (the default): relative when it is within a week of now — "2 hours
 *   ago", "in 3 days" — and the date and time otherwise, because "5 months ago"
 *   answers no question anybody asks of an order.
 * - `datetime`, `date`, `time`: always absolute.
 *
 * Whatever is shown, the exact moment with its time zone is on hover and in the
 * `<time>` element's machine-readable value, so a relative time can always be
 * pinned down. The zone is the one the surrounding `TimeZoneProvider` set.
 */
export function Timestamp({
  className,
  mode = "auto",
  value,
}: {
  className?: string;
  mode?: "auto" | "datetime" | "date" | "time";
  value: string | number | Date | null | undefined;
}) {
  const format = useFormatter();
  // Re-rendered each minute so "just now" does not stay "just now".
  const now = useNow({ updateInterval: 60_000 });

  if (value === null || value === undefined || value === "") {
    return <span className={className}>—</span>;
  }
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) {
    return <span className={className}>—</span>;
  }

  const relative = mode === "auto" && Math.abs(now.getTime() - date.getTime()) < RELATIVE_WINDOW_MS;
  const text = relative
    ? format.relativeTime(date, now)
    : format.dateTime(date, mode === "auto" ? "datetime" : mode);

  return (
    <time
      className={className}
      dateTime={date.toISOString()}
      // The server and the browser read the clock a moment apart, so a
      // relative time may differ by a word between the two renders.
      suppressHydrationWarning
      title={format.dateTime(date, "exact")}
    >
      {text}
    </time>
  );
}
