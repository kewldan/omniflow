"use client";

import { useCallback } from "react";

import { apiFetch } from "./api";
import { useSession } from "./session";

/** The closed set the API accepts; anything else is dropped server-side. */
export type Preferences = {
  pageSize?: number;
  density?: "compact" | "comfortable";
  auditSort?: "asc" | "desc";
  auditCategory?: string;
};

/**
 * Operator preferences that follow the account rather than the browser.
 *
 * They live on the server, not in localStorage, so an operator who signs in
 * from a second machine finds the panel as they left it. Saves are fire and
 * forget: a preference that fails to persist is not worth interrupting the work
 * that triggered it, and the session refresh reconciles the truth.
 */
export function usePreferences() {
  const { session, refresh } = useSession();
  const preferences = (session?.account.preferences ?? {}) as Preferences;

  const save = useCallback(
    async (patch: Preferences) => {
      try {
        await apiFetch("/v1/panel/auth/preferences", {
          body: JSON.stringify(patch),
          method: "PUT",
        });
        await refresh();
      } catch {
        // Deliberately silent: see above.
      }
    },
    [refresh],
  );

  return { preferences, save };
}
