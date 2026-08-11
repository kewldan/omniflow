"use client";

import { createContext, type ReactNode, useCallback, useContext, useMemo } from "react";
import useSWR from "swr";

import { type ApiError, fetcher, setCsrfToken } from "./api";

/** Mirrors the PanelSession schema in api/openapi.yaml. */
export type PanelAccount = {
  id: string;
  email: string;
  displayName: string;
  status: "active" | "suspended" | "disabled";
  locale: "en" | "ru";
  timezone: string;
  roles: string[];
  totpEnabled: boolean;
  lastLoginAt?: string;
  createdAt: string;
};

export type PanelSession = {
  account: PanelAccount;
  permissions: string[];
  csrfToken: string;
  sessionId: string;
  expiresAt: string;
  remainingRecoveryCodes?: number;
};

type SessionState = {
  session: PanelSession | null;
  /** True while the first load is in flight and nothing is known yet. */
  loading: boolean;
  /** Set when the session could not be loaded for a reason other than "signed out". */
  error: ApiError | null;
  /** True when the server answered 401 — the operator is simply not signed in. */
  signedOut: boolean;
  /** True while a background revalidation is refreshing a session already shown. */
  stale: boolean;
  can: (permission: string) => boolean;
  canAll: (...permissions: string[]) => boolean;
  refresh: () => Promise<unknown>;
};

const SessionContext = createContext<SessionState | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const { data, error, isLoading, isValidating, mutate } = useSWR<PanelSession, ApiError>(
    "/v1/panel/auth/session",
    fetcher,
    {
      // The session is the panel's root dependency, so it is revalidated when
      // the operator returns to the tab: a session revoked elsewhere should
      // stop working here promptly rather than at the next mutation.
      revalidateOnFocus: true,
      revalidateOnReconnect: true,
      shouldRetryOnError: (retryError) => retryError.status !== 401,
    },
  );

  if (data?.csrfToken) {
    setCsrfToken(data.csrfToken);
  }

  const permissions = useMemo(() => new Set(data?.permissions ?? []), [data?.permissions]);
  const can = useCallback((permission: string) => permissions.has(permission), [permissions]);
  const canAll = useCallback(
    (...required: string[]) => required.every((permission) => permissions.has(permission)),
    [permissions],
  );

  const value = useMemo<SessionState>(
    () => ({
      session: data ?? null,
      loading: isLoading,
      error: error && error.status !== 401 ? error : null,
      signedOut: error?.status === 401,
      stale: isValidating && Boolean(data),
      can,
      canAll,
      refresh: mutate,
    }),
    [can, canAll, data, error, isLoading, isValidating, mutate],
  );

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionState {
  const context = useContext(SessionContext);
  if (!context) {
    throw new Error("useSession must be used inside a SessionProvider");
  }
  return context;
}

/**
 * Convenience hook for a single permission.
 *
 * Hiding a control is presentation only — the API enforces the same permission
 * independently, so a hidden route is never the thing standing between an
 * operator and an action they are not allowed to take.
 */
export function usePermission(permission: string): boolean {
  return useSession().can(permission);
}
