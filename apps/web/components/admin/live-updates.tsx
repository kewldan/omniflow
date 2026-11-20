"use client";

import { useEffect } from "react";
import { useSWRConfig } from "swr";

/** Which cached reads each live topic refreshes. */
const PREFIXES: Record<string, string[]> = {
  support: ["/v1/panel/support"],
  finance: ["/v1/panel/finance", "/v1/panel/overview"],
  errors: ["/v1/panel/system/errors"],
  customers: ["/v1/panel/customers"],
};

/**
 * Keeps open panel screens current. The API streams which areas changed —
 * never the records themselves — and this revalidates the cached reads in
 * those areas, so a new ticket message, a paid order, or a new error shows up
 * without reloading, through the same permission-checked routes the screen
 * already uses. EventSource reconnects on its own when the stream ends.
 */
export function LiveUpdates() {
  const { mutate } = useSWRConfig();
  useEffect(() => {
    if (typeof EventSource === "undefined") {
      return;
    }
    const source = new EventSource("/v1/panel/live", { withCredentials: true });
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
    });
    return () => source.close();
  }, [mutate]);
  return null;
}
