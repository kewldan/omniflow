"use client";

import { useTranslations } from "next-intl";
import { useCallback } from "react";

import { ApiError } from "@/lib/api";

/**
 * Turning a refused request into a sentence the customer can act on.
 *
 * Every area of the customer panel does the same thing: read the problem's
 * `code` (the transport derives it from the problem type URI, and it is the
 * only part of a problem document the panel treats as a contract), look up copy
 * for it in that area's catalogue, and fall back to one general sentence. The
 * server's own `detail` is never shown — it is written in English by a Go
 * package with no locale, for an operator reading a log.
 *
 * There used to be a copy of this per area. They differed only in where the
 * copy lives and in one detail apiece, which is all the options below express.
 */
export function useProblemCopy(
  namespace: string,
  options: {
    /** The group under the namespace holding one sentence per code. */
    group: string;
    /**
     * The codes that have copy. A closed list is the contract: a code missing
     * here falls back to `unknown` rather than to whatever key happens to exist.
     * Omitted, any code with copy in the group is used.
     */
    known?: readonly string[];
    /**
     * Copy for a request that never reached the API, where "try again" is the
     * right advice — unlike a refusal the server will repeat.
     */
    offline?: string;
  },
): (error: unknown) => string {
  const translate = useTranslations(namespace);
  const { group, known, offline } = options;
  return useCallback(
    (error: unknown) => {
      if (offline && error !== undefined && !(error instanceof ApiError)) {
        return translate(`${group}.${offline}`);
      }
      const code = problemCode(error);
      const listed = known ? known.includes(code) : translate.has(`${group}.${code}`);
      return translate(`${group}.${code && listed ? code : "unknown"}`);
    },
    [group, known, offline, translate],
  );
}

/** Reads the machine code off a failed request, for branching on the outcome. */
export function problemCode(error: unknown): string {
  return (error as ApiError | undefined)?.code?.trim() ?? "";
}

/**
 * The privacy and loyalty routes.
 *
 * `contact_unavailable` deserves the note. The server refuses to say whether an
 * address belongs to another account, and the copy keeps that promise: it says
 * the address cannot be added here and points at support, where a person can
 * establish who is asking. Wording it as "already in use" would turn the panel
 * into a way of testing whether somebody has an account.
 */
const ACCOUNT_CODES = [
  "confirmation_required",
  "contact_unavailable",
  "contacts_unavailable",
  "deletion_pending",
  "invalid_input",
  "no_deletion_pending",
  "not_found",
  "rate_limited",
  "reauthentication_required",
] as const;

export function useAccountProblem(): (error: unknown) => string {
  return useProblemCopy("account.account", { group: "errors", known: ACCOUNT_CODES });
}

/**
 * The shop routes (`writeShopError`). A code without copy degrades to a
 * sentence that at least says nothing was charged.
 */
const SHOP_CODES = [
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
] as const;

export function useShopProblem(): (error: unknown) => string {
  return useProblemCopy("account.shop", { group: "problems", known: SHOP_CODES });
}

/** Support, news, and notification preferences, whose copy is keyed by code. */
export function useSupportProblem(): (error: unknown) => string {
  return useProblemCopy("account.support", { group: "problem" });
}
