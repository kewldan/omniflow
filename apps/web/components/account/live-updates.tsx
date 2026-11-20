"use client";

import { useEffect } from "react";
import { useSWRConfig } from "swr";

/** Which cached reads each live topic refreshes. */
const PREFIXES: Record<string, string[]> = {
  support: ["/v1/account/support"],
  orders: [
    "/v1/account/orders",
    "/v1/account/checkout",
    "/v1/account/shop",
    "/v1/account/overview",
    "/v1/account/wallet",
  ],
  subscriptions: ["/v1/account/overview", "/v1/account/subscriptions", "/v1/account/renewal"],
  wallet: ["/v1/account/wallet", "/v1/account/overview"],
};

/** The window event a paged list listens for; see `useAccountLive`. */
export const ACCOUNT_LIVE_EVENT = "omniflow:account-live";

/**
 * Runs `refresh` whenever the live stream reports a change in `topic`.
 *
 * A filtered global `mutate` skips `useSWRInfinite` lists by design, so a paged
 * list — the support conversations — subscribes here and revalidates itself.
 */
export function useAccountLive(topic: string, refresh: () => void) {
  useEffect(() => {
    const listener = (event: Event) => {
      if ((event as CustomEvent<string[]>).detail?.includes(topic)) {
        refresh();
      }
    };
    window.addEventListener(ACCOUNT_LIVE_EVENT, listener);
    return () => window.removeEventListener(ACCOUNT_LIVE_EVENT, listener);
  }, [topic, refresh]);
}

/**
 * Keeps the customer's open screens current. The API streams which areas of
 * the customer's own panel changed — a support reply, an order or payment
 * moving, a subscription being provisioned or renewed, a wallet entry — and
 * never the records themselves; this revalidates the cached reads in those
 * areas, so an operator's answer appears in an open conversation within
 * seconds. EventSource reconnects on its own and sends back the versions it
 * last saw, so nothing that changed while it was reconnecting is missed.
 */
export function AccountLiveUpdates() {
  const { mutate } = useSWRConfig();
  useEffect(() => {
    if (typeof EventSource === "undefined") {
      return;
    }
    const source = new EventSource("/v1/account/live", { withCredentials: true });
    source.addEventListener("change", (event) => {
      let topics: string[] = [];
      try {
        topics = Object.keys(JSON.parse((event as MessageEvent<string>).data));
      } catch {
        return;
      }
      const prefixes = topics.flatMap((topic) => PREFIXES[topic] ?? []);
      if (prefixes.length > 0) {
        void mutate(
          (key) => typeof key === "string" && prefixes.some((prefix) => key.startsWith(prefix)),
        );
      }
      window.dispatchEvent(new CustomEvent(ACCOUNT_LIVE_EVENT, { detail: topics }));
    });
    return () => source.close();
  }, [mutate]);
  return null;
}
