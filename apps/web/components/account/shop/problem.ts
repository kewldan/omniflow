"use client";

import { useTranslations } from "next-intl";
import { useCallback } from "react";

import type { ApiError } from "@/lib/api";

/**
 * The problem codes `writeShopError` can answer with.
 *
 * Every one of them is listed rather than pattern-matched, because the list is
 * the contract: a code that appears here has copy naming the customer's next
 * step, and a code that does not falls back to a sentence that at least says
 * nothing was charged. Adding a refusal to the Go handler and forgetting the
 * copy therefore degrades to a truthful message instead of rendering a raw
 * server string.
 */
const KNOWN = new Set([
  "idempotency_key_required",
  "invalid_input",
  "maintenance_active",
  "not_found",
  "price_changed",
  "price_unavailable",
  "promo_below_cost",
  "promo_exhausted",
  "promo_ineligible",
  "promo_unknown",
  "quote_expired",
  "recipient_invalid",
  "recipient_not_reviewed",
  "shop_unavailable",
]);

/**
 * Turns a refusal into a sentence the customer can act on.
 *
 * The server's own `detail` is deliberately never shown. It is written in
 * English by the Go handler, and a Russian-reading customer being handed an
 * English fragment mid-purchase is worse than a slightly more general sentence
 * in their own language.
 */
export function useShopProblem(): (error: unknown) => string {
  const translate = useTranslations("account.shop");
  return useCallback(
    (error: unknown) => {
      const code = (error as ApiError | undefined)?.code ?? "";
      return translate(`problems.${KNOWN.has(code) ? code : "unknown"}`);
    },
    [translate],
  );
}

/** Reads the machine code off a failed request, for branching on the outcome. */
export function problemCode(error: unknown): string {
  return (error as ApiError | undefined)?.code ?? "";
}
