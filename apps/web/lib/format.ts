"use client";

import { useLocale } from "next-intl";
import { useCallback, useMemo } from "react";

import { currencyExponent, formatMoney, toMajorUnits } from "./money";

/**
 * The customer panel's value formatting, bound to the customer's locale.
 *
 * Money itself is formatted by `lib/money.ts`, which the operator panel shares,
 * so the two panels can never again disagree about how many decimals a
 * currency has.
 */

export { currencyExponent, toMajorUnits };

export type MoneyFormatter = (minor: number, currency: string) => string;

/** Formats an integer minor-unit amount in the customer's own locale. */
export function useMoney(): MoneyFormatter {
  const locale = useLocale();
  return useCallback(
    (minor: number, currency: string) =>
      formatMoney(minor, currency, locale, { display: "narrowSymbol" }),
    [locale],
  );
}

const BYTE_UNITS = ["B", "KB", "MB", "GB", "TB", "PB"] as const;

/**
 * Formats a byte count the way a traffic allowance is normally quoted.
 *
 * Binary multiples are used because that is what the upstream panel reports and
 * what a plan's allowance is written in; showing a 100 GiB allowance as 107 GB
 * would make the number on the invoice and the number on the bar disagree.
 */
export function formatBytes(bytes: number, locale = "en"): string {
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return "0 B";
  }
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const digits = value >= 100 || unit === 0 ? 0 : 1;
  return `${new Intl.NumberFormat(locale, {
    maximumFractionDigits: digits,
    minimumFractionDigits: 0,
  }).format(value)} ${BYTE_UNITS[unit]}`;
}

/** The byte formatter bound to the customer's locale. */
export function useBytes(): (bytes: number) => string {
  const locale = useLocale();
  return useCallback((bytes: number) => formatBytes(bytes, locale), [locale]);
}

/**
 * Renders a duration in whole days, hours, or minutes.
 *
 * Used for a quote expiry and a grace period, where the customer needs to know
 * how long they have rather than the exact instant it lapses. It rounds down, so
 * a countdown never claims more time than remains.
 */
export function useDuration(): (
  untilISO: string,
  from?: Date,
) => {
  expired: boolean;
  minutes: number;
} {
  return useCallback((untilISO: string, from = new Date()) => {
    const remaining = new Date(untilISO).getTime() - from.getTime();
    return { expired: remaining <= 0, minutes: Math.max(0, Math.floor(remaining / 60_000)) };
  }, []);
}

/**
 * A stable percentage for a progress bar, clamped so a server that reports more
 * used than allowed cannot paint a bar past its own track.
 */
export function clampPercent(value: number): number {
  if (!Number.isFinite(value)) {
    return 0;
  }
  return Math.min(100, Math.max(0, Math.round(value)));
}

/** Groups a list by a key, preserving the order each group first appeared in. */
export function groupBy<Item, Key extends string>(
  items: readonly Item[],
  key: (item: Item) => Key,
): Array<[Key, Item[]]> {
  const groups = new Map<Key, Item[]>();
  for (const item of items) {
    const group = key(item);
    const existing = groups.get(group);
    if (existing) {
      existing.push(item);
    } else {
      groups.set(group, [item]);
    }
  }
  return [...groups.entries()];
}

/** Memoised money and byte formatters for a component that needs both. */
export function useFormatters(): { bytes: (value: number) => string; money: MoneyFormatter } {
  const money = useMoney();
  const bytes = useBytes();
  return useMemo(() => ({ bytes, money }), [bytes, money]);
}
