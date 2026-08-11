"use client";

import { createContext, type ReactNode, useContext, useMemo } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "./api";

/** Mirrors the AccountCustomer schema in api/openapi.yaml. */
export type AccountCustomer = {
  id: string;
  locale: "en" | "ru";
  timezone: string;
  status: "active" | "suspended" | "deleted";
};

/** Mirrors the AccountSession schema in api/openapi.yaml. */
export type AccountSession = {
  customer: AccountCustomer;
  session: {
    id: string;
    authMethod: "telegram" | "magic_link" | "oidc";
    authProvider?: string;
    expiresAt: string;
    /**
     * The session is older than the re-authentication window, so a destructive
     * action will be refused until the customer signs in again. The panel reads
     * it to explain that up front rather than after a failed request.
     */
    reauthenticationRequired: boolean;
  };
};

type AccountState = {
  session: AccountSession | null;
  /** True while the first load is in flight and nothing is known yet. */
  loading: boolean;
  /** Set when the session could not be loaded for a reason other than "signed out". */
  error: ApiError | null;
  /** True when the server answered 401 — the customer is simply not signed in. */
  signedOut: boolean;
  /** True when the account itself is suspended or deleted, which is not the same thing. */
  unavailable: boolean;
  /** True while a background revalidation refreshes a session already on screen. */
  stale: boolean;
  refresh: () => Promise<unknown>;
};

const AccountContext = createContext<AccountState | null>(null);

/**
 * Loads the signed-in customer and holds it for the whole panel.
 *
 * The CSRF token needs no handling here: the API publishes it on the
 * `X-CSRF-Token` response header and the shared transport captures it from every
 * response, so a token rotated mid-session is already current by the time the
 * next mutation is submitted.
 */
export function AccountProvider({ children }: { children: ReactNode }) {
  const { data, error, isLoading, isValidating, mutate } = useSWR<AccountSession, ApiError>(
    "/v1/account/me",
    fetcher,
    {
      // The session is the panel's root dependency, so it revalidates when the
      // customer returns to the tab: a session ended from another device should
      // stop working here promptly rather than at the next mutation.
      revalidateOnFocus: true,
      revalidateOnReconnect: true,
      // 401 means "not signed in" and 403 means "account unavailable". Neither
      // is worth retrying, and retrying either would spin against a wall.
      shouldRetryOnError: (retryError) => retryError.status !== 401 && retryError.status !== 403,
    },
  );

  const value = useMemo<AccountState>(
    () => ({
      error: error && error.status !== 401 && error.status !== 403 ? error : null,
      loading: isLoading,
      refresh: mutate,
      session: data ?? null,
      signedOut: error?.status === 401,
      stale: Boolean(data) && isValidating,
      unavailable: error?.status === 403,
    }),
    [data, error, isLoading, isValidating, mutate],
  );

  return <AccountContext.Provider value={value}>{children}</AccountContext.Provider>;
}

export function useAccount(): AccountState {
  const state = useContext(AccountContext);
  if (!state) {
    throw new Error("useAccount must be used inside an AccountProvider");
  }
  return state;
}
