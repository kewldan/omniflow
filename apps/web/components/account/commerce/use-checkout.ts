"use client";

import { useEffect, useState } from "react";
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
export function useOpenCheckout({ fresh = false }: { fresh?: boolean } = {}): {
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

  // `fresh` is for the checkout screen itself. The store's plan page reads this
  // same key, gets the 404 of "nothing open yet", then creates the checkout and
  // navigates here — and SWR served that cached 404 at once (and, inside its
  // two-second dedupe window, without asking again), so the screen said "No
  // purchase in progress" about the purchase just started. A fresh read asks
  // the API on arrival and counts as loading until it has answered.
  const [settled, setSettled] = useState(!fresh);
  useEffect(() => {
    if (!fresh) {
      return;
    }
    let current = true;
    void mutate().finally(() => {
      if (current) {
        setSettled(true);
      }
    });
    return () => {
      current = false;
    };
  }, [fresh, mutate]);

  // SWR keeps the last successful body in the cache when a request fails, which
  // is right for a flaky read and wrong for this one: a checkout that was
  // discarded, confirmed, or left to lapse answers 404 for good, and serving the
  // cached copy beside that 404 would leave a "resume your purchase" banner
  // pointing at something that no longer exists.
  const notFound = error?.status === 404;
  const missing = settled && notFound;

  return {
    checkout: notFound ? null : (data ?? null),
    error: settled && error && !notFound ? error : null,
    loading: isLoading || !settled,
    missing,
    mutate,
  };
}
