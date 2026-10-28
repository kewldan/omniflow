"use client";

import { useCallback, useEffect, useState } from "react";
import { useSWRConfig } from "swr";

/**
 * Bounded polling for the screens that wait on something the customer cannot
 * hurry: a payment webhook, a provisioning job, a delivery.
 *
 * Both waiting screens used to poll at a fixed interval with no end: an order
 * stuck in `provisioning` asked the API every six seconds for as long as the
 * tab stayed open, and the connect screen retried its 409 every five seconds
 * forever. Neither ever told the customer that the wait had become unusual.
 *
 * Now the interval starts short, because most waits end in seconds, and grows
 * with the time already spent, because a wait that has lasted minutes will not
 * end in the next three seconds. Past `slowAfterMs` the screen says it is taking
 * longer than usual and offers support; past `ceilingMs` polling stops and the
 * customer checks again by hand.
 */
export type PollingPolicy = {
  initialMs: number;
  maxMs: number;
  slowAfterMs: number;
  ceilingMs: number;
};

export const POLLING: PollingPolicy = {
  ceilingMs: 15 * 60_000,
  initialMs: 3_000,
  maxMs: 30_000,
  slowAfterMs: 2 * 60_000,
};

/**
 * The wait before the next poll, given how long polling has gone on; 0 once
 * the ceiling is reached, which SWR reads as "stop".
 */
export function nextPollDelay(elapsedMs: number, policy: PollingPolicy = POLLING): number {
  if (elapsedMs >= policy.ceilingMs) {
    return 0;
  }
  // A tenth of the time already waited, within bounds: 3 s at first, 6 s after
  // a minute, 30 s from five minutes on.
  return Math.round(Math.min(policy.maxMs, Math.max(policy.initialMs, elapsedMs / 10)));
}

export type BoundedPolling = {
  /** The interval to hand SWR's `refreshInterval`; 0 when not polling. */
  delay: number;
  /** The wait has gone on longer than it normally does. */
  slow: boolean;
  /** Polling has stopped; the customer checks again by hand. */
  stopped: boolean;
  /** Starts the clock again, after a manual "check again". */
  restart: () => void;
};

/** Tracks one bounded wait; `active` is whether there is anything to wait for. */
export function useBoundedPolling(
  active: boolean,
  policy: PollingPolicy = POLLING,
): BoundedPolling {
  const [startedAt, setStartedAt] = useState<number | null>(null);
  const [now, setNow] = useState(0);

  useEffect(() => {
    if (active && startedAt === null) {
      const started = Date.now();
      setStartedAt(started);
      setNow(started);
    } else if (!active && startedAt !== null) {
      setStartedAt(null);
    }
  }, [active, startedAt]);

  // The clock only has to be fine enough to flip `slow` and `stopped`.
  useEffect(() => {
    if (startedAt === null) {
      return;
    }
    const timer = window.setInterval(() => setNow(Date.now()), 5_000);
    return () => window.clearInterval(timer);
  }, [startedAt]);

  const restart = useCallback(() => {
    const started = Date.now();
    setStartedAt(started);
    setNow(started);
  }, []);

  const elapsed = startedAt === null ? 0 : Math.max(0, now - startedAt);
  return {
    delay: active ? nextPollDelay(elapsed, policy) : 0,
    restart,
    slow: active && elapsed >= policy.slowAfterMs,
    stopped: active && elapsed >= policy.ceilingMs,
  };
}

/** The account resources a payment, a cancellation, or a top-up can change. */
const COMMERCE_KEYS = [
  "/v1/account/orders",
  "/v1/account/subscriptions",
  "/v1/account/wallet",
  "/v1/account/checkout",
  "/v1/account/shop/orders",
];

/**
 * True when an SWR key reads one of them. A `swr/infinite` list is never
 * offered to a filter by SWR, so a screen holding one re-reads it itself.
 */
export function isCommerceKey(key: unknown): boolean {
  const text = typeof key === "string" ? key : Array.isArray(key) ? String(key[0]) : "";
  return COMMERCE_KEYS.some((prefix) => text.includes(prefix));
}

/**
 * Re-reads every cached view that money moving could have changed.
 *
 * Paying, cancelling, or topping up changes more than the screen it happened
 * on: the order list, the subscription the order extends, the wallet balance
 * a reservation came out of. Without this, going back to any of them showed
 * the state from before until SWR happened to revalidate it.
 */
export function useRevalidateCommerce(): () => Promise<unknown> {
  const { mutate } = useSWRConfig();
  return useCallback(() => mutate(isCommerceKey), [mutate]);
}
