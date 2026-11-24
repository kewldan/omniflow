"use client";

import { Input } from "@omniflow/ui/input";
import { cn } from "@omniflow/ui/lib/utils";
import { useLocale } from "next-intl";
import type { ComponentProps } from "react";

import { formatMoney } from "@/lib/money";

/**
 * The amount a minor-unit entry stands for, written the way a customer reads
 * it: "10000" in RUB is "100 ₽". Empty when the text is not a whole number of
 * minor units, so a half-typed value shows nothing rather than a wrong sum.
 */
export function minorAmountLabel(text: string, currency: string, locale: string): string {
  const trimmed = text.trim();
  if (!/^\d{1,15}$/.test(trimmed) || !/^[A-Za-z]{3}$/.test(currency.trim())) {
    return "";
  }
  return formatMoney(Number(trimmed), currency.trim(), locale, { trimZeroFraction: true });
}

/**
 * A field that still takes minor units — kopecks, cents — and shows inside it,
 * on the right, the sum that number is. Settings stored as minor-unit integers
 * keep their wire form here; the grey "100 ₽" is what stops an operator from
 * typing 100 and selling a top-up for a rouble.
 */
export function MinorAmountInput({
  className,
  currency,
  value,
  ...props
}: Omit<ComponentProps<typeof Input>, "type" | "inputMode" | "value"> & {
  currency: string;
  value: string;
}) {
  const locale = useLocale();
  const amount = minorAmountLabel(value, currency, locale);
  return (
    <div className="relative">
      <Input
        autoComplete="off"
        className={cn("tabular-nums", amount && "pr-28", className)}
        inputMode="numeric"
        type="text"
        value={value}
        {...props}
      />
      {amount && (
        <span
          aria-hidden
          className="pointer-events-none absolute inset-y-0 right-3 flex max-w-24 items-center truncate text-muted-foreground text-xs tabular-nums"
        >
          {amount}
        </span>
      )}
    </div>
  );
}

/**
 * The sums a comma-separated list of minor-unit amounts stands for, one line
 * under the field: "100 ₽ · 300 ₽ · 500 ₽". Entries that are not whole
 * numbers are left out.
 */
export function MinorAmountList({ currency, value }: { currency: string; value: string }) {
  const locale = useLocale();
  const amounts = value
    .split(",")
    .map((entry) => minorAmountLabel(entry, currency, locale))
    .filter(Boolean);
  if (amounts.length === 0) {
    return null;
  }
  return <p className="text-muted-foreground text-xs tabular-nums">{amounts.join(" · ")}</p>;
}
