import type { Money } from "./types";

/**
 * How a longer period is weighed against the plan's own: by the price per
 * day.
 *
 * One rule for the store card's switcher and the checkout's picker, so the
 * "−50%" a customer chose on the card is the figure the checkout shows them
 * again. It is a reading of prices the server already set, not a price: the
 * checkout's quote is still the only total.
 */

/** Savings under this many per cent are left unsaid; "−2%" is noise. */
export const SAVING_SHOWN = 5;

/** The plan's own period in whole days, never zero. */
export function ownPeriodDays(durationSeconds: number): number {
  return Math.max(1, Math.round(durationSeconds / 86_400));
}

/**
 * The per-day saving of `days` at `price` against the plan's own period, in
 * whole per cent; zero when the two are in different currencies or the plan's
 * own period is free, since neither comparison means anything.
 */
export function periodSaving(
  own: { durationSeconds: number; price: Money },
  days: number,
  price: Money,
): number {
  const ownPerDay = own.price.amountMinor / ownPeriodDays(own.durationSeconds);
  if (ownPerDay <= 0 || days <= 0 || price.currency !== own.price.currency) {
    return 0;
  }
  return Math.round((1 - price.amountMinor / days / ownPerDay) * 100);
}
