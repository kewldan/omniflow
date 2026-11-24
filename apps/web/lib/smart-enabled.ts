"use client";

import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";

/**
 * Whether the operator runs the smart subscription. Kept apart from the
 * screen itself so the cabinet's menu can ask without loading it.
 */
export function useSmartEnabled(): boolean {
  const { data } = useSWR<{ enabled: boolean }, ApiError>(
    "/v1/account/smart-subscription",
    fetcher,
    {
      // A module the installation does not run answers 404; that is "off".
      shouldRetryOnError: false,
    },
  );
  return data?.enabled ?? false;
}
