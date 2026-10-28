import type { Formats } from "next-intl";

/**
 * The named date formats both panels use, so a moment reads the same way on
 * every screen: the locale's own order and clock (24-hour in Russian), never
 * the browser's default locale, which is what `toLocaleString()` with no
 * argument silently used and why an operator in a Russian panel saw
 * "8/15/2026, 1:16:37 AM".
 *
 * `exact` carries the time zone's name and is what a relative time shows on
 * hover, so "2 hours ago" can always be pinned to a zone.
 */
export const formats = {
  dateTime: {
    date: { dateStyle: "medium" },
    datetime: { dateStyle: "medium", timeStyle: "short" },
    exact: { dateStyle: "long", timeStyle: "long" },
    time: { timeStyle: "short" },
  },
} satisfies Formats;
