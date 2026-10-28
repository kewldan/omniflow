"use client";

import { Input } from "@omniflow/ui/input";
import { cn } from "@omniflow/ui/lib/utils";
import type { ComponentProps } from "react";

import { currencyExponent, isTelegramStars } from "@/lib/money";

/**
 * An amount typed in the currency's major unit — "4.99", not "499".
 *
 * Every amount in Omniflow is an integer of minor units on the wire, and the
 * panels used to ask operators to type that integer. It is a trap: "4.99" in a
 * minor-unit field is either refused or, worse, read as 4 kopecks, and the
 * exponent differs by currency (none for JPY and Stars, three for KWD). People
 * type prices the way they read them, so the field takes major units and the
 * form converts with `parseMajorToMinor`, which knows the exponent.
 *
 * The currency is shown inside the field so the unit is never in doubt, and the
 * placeholder shows how many decimals the currency takes.
 */
export function MoneyInput({
  className,
  currency,
  ...props
}: Omit<ComponentProps<typeof Input>, "type" | "inputMode"> & { currency: string }) {
  const code = currency.trim().toUpperCase();
  const known = /^[A-Z]{3}$/.test(code);
  const exponent = known ? currencyExponent(code) : 2;
  const placeholder = exponent === 0 ? "0" : `0.${"0".repeat(exponent)}`;
  const unit = known ? (isTelegramStars(code) ? "⭐" : code) : "";

  return (
    <div className="relative">
      <Input
        autoComplete="off"
        className={cn("pr-14 tabular-nums", className)}
        inputMode={exponent === 0 ? "numeric" : "decimal"}
        placeholder={placeholder}
        type="text"
        {...props}
      />
      {unit && (
        <span
          aria-hidden
          className="pointer-events-none absolute inset-y-0 right-3 flex items-center font-mono text-[11px] text-muted-foreground"
        >
          {unit}
        </span>
      )}
    </div>
  );
}
