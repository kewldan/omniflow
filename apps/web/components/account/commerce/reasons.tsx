"use client";

import { useTranslations } from "next-intl";
import { useCallback } from "react";

import { ApiError } from "@/lib/api";

/**
 * Turning the API's stable machine reasons into the customer's language.
 *
 * The API answers a refused promo code, an ineligible plan, a rejected top-up,
 * and a blocked lifecycle change with a value rather than a sentence —
 * `promo_exhausted`, `subscription_limit_reached`, `topup_below_minimum` — for
 * exactly one purpose: both surfaces look up their own Russian and English copy
 * for it. Rendering the server's `detail` string instead would put untranslated
 * English on a Russian screen, which is what these values exist to prevent.
 *
 * Every lookup goes through a known-value list. A server one version ahead can
 * send a reason this build has never heard of, and the honest response to that is
 * a sentence admitting the refusal happened without inventing a cause for it —
 * not a missing-message crash, and certainly not a raw identifier shown to a
 * customer.
 */

/** Promo rejections, from `PromoUnknown` and its siblings in accountcheckout. */
const PROMO_REJECTIONS = [
  "promo_unknown",
  "promo_ineligible",
  "promo_exhausted",
  "promo_invalid",
] as const;

/**
 * Plan ineligibility, from `applyEligibility`.
 *
 * A trial carries the raw reason `commerce.EvaluateTrial` returned, while a
 * plan refused for concurrency carries `subscription_limit_reached`. They share
 * one vocabulary here because they arrive in one field.
 */
const INELIGIBILITY = [
  "account_too_new",
  "existing_customer",
  "identity_already_trialled",
  "not_a_trial_plan",
  "subscription_active",
  "subscription_limit_reached",
  "trial_already_used",
  "unsupported_trial_rule",
] as const;

/**
 * The RFC 9457 `type` codes the commerce routes answer with.
 *
 * The list is taken from `writeCheckoutError` and the `writeAccountError` it
 * falls through to, including the reasons the domain wraps: squad selection,
 * top-up limits, subscription concurrency, and trial evaluation. The trial cases
 * appear twice because the handler prefixes the domain reason with `trial_`, so
 * `trial_already_used` arrives as `trial_trial_already_used` from the confirm
 * path and as `trial_already_used` from the store's own guard.
 */
const PROBLEMS = [
  "addon_unavailable",
  "channel_required",
  "checkout_settled",
  "idempotency_key_required",
  "invalid_cursor",
  "invalid_input",
  "maintenance_active",
  "multi_subscription_disabled",
  "no_active_subscription",
  "no_checkout",
  "not_found",
  "not_provisioned",
  "operation_forbidden",
  "order_not_cancellable",
  "order_not_payable",
  "payment_failed",
  "payment_not_required",
  "plan_limit_reached",
  "plan_unavailable",
  "promo_exhausted",
  "promo_ineligible",
  "promo_invalid",
  "promo_unknown",
  "provider_currency_unsupported",
  "provider_unavailable",
  "request_failed",
  "squad_not_offered",
  "squad_selection_not_allowed",
  "squad_selection_required",
  "squad_selection_too_few",
  "squad_selection_too_many",
  "subscription_limit_reached",
  "subscription_target_required",
  "topup_above_maximum",
  "topup_below_minimum",
  "topup_disabled",
  "topup_invalid_amount",
  "topup_window_exceeded",
  "trial_account_too_new",
  "trial_already_used",
  "trial_existing_customer",
  "trial_identity_already_trialled",
  "trial_not_eligible",
  "trial_subscription_active",
  "trial_trial_already_used",
  "upstream_unavailable",
] as const;

/** Builds a lookup that falls back rather than crashing on an unseen value. */
function useCodeCopy(group: string, known: readonly string[]): (code?: string) => string {
  const translate = useTranslations("account.commerce");
  return useCallback(
    (code?: string) => {
      const value = code?.trim() ?? "";
      return translate(`${group}.${known.includes(value) ? value : "unknown"}`);
    },
    [group, known, translate],
  );
}

/** Explains why a promo code was not applied. */
export function usePromoRejection(): (code?: string) => string {
  return useCodeCopy("promoRejection", PROMO_REJECTIONS);
}

/** Explains why a plan cannot be started by this customer. */
export function useIneligibility(): (code?: string) => string {
  return useCodeCopy("eligibility", INELIGIBILITY);
}

/**
 * Explains a problem code the server carried in a 200 body rather than in a
 * refusal — the checkout's unfinished server choice, for one — using the same
 * copy a refusal with that code would get.
 */
export function useProblemCode(): (code?: string) => string {
  return useCodeCopy("problems", PROBLEMS);
}

/**
 * Explains a failed mutation.
 *
 * A transport failure — the request never reached the API — is deliberately its
 * own message: "try again" is the right advice there and the wrong advice for a
 * refusal the server will repeat.
 */
export function useProblemMessage(): (error: unknown) => string {
  const translate = useTranslations("account.commerce");
  const problem = useCodeCopy("problems", PROBLEMS);
  return useCallback(
    (error: unknown) => {
      if (error instanceof ApiError) {
        return problem(error.code);
      }
      return translate("problems.offline");
    },
    [problem, translate],
  );
}
