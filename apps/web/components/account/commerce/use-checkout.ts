"use client";

import useSWR from "swr";

import type { CheckoutView } from "@/components/account/commerce/types";
import { type ApiError, fetcher } from "@/lib/api";

/**
 * Reads the customer's one open checkout.
 *
 * `GET /v1/account/checkout` answers 404 when nothing is in progress, and that
 * is a normal state rather than a failure — most visits to the store have no
 * checkout behind them. Every screen that asks would otherwise have to remember
 * not to render an error for it, so the distinction is made once, here: `missing`
 * is "you have nothing open", `error` is "something went wrong", and only the
 * second is worth a warning.
 *
 * Retrying a 404 is pointless and would hammer the API from the catalogue on
 * every visit, so it is excluded from the retry policy rather than merely
 * ignored in the render.
 */
export function useOpenCheckout(): {
  checkout: CheckoutView | null;
  error: ApiError | null;
  loading: boolean;
  missing: boolean;
  mutate: (
    value?: CheckoutView | Promise<CheckoutView>,
    options?: { revalidate?: boolean },
  ) => Promise<unknown>;
} {
  const { data, error, isLoading, mutate } = useSWR<CheckoutView, ApiError>(
    "/v1/account/checkout",
    fetcher,
    { shouldRetryOnError: (retryError) => retryError.status !== 404 },
  );

  // SWR keeps the last successful body in the cache when a request fails, which
  // is right for a flaky read and wrong for this one: a checkout that was
  // discarded, confirmed, or left to lapse answers 404 for good, and serving the
  // cached copy beside that 404 would leave a "resume your purchase" banner
  // pointing at something that no longer exists.
  const missing = error?.status === 404;

  return {
    checkout: missing ? null : (data ?? null),
    error: error && !missing ? error : null,
    loading: isLoading,
    missing,
    mutate,
  };
}
