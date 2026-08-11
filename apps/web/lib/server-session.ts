import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import type { PanelSession } from "./session";

/**
 * Server-side session and permission checks for the panel.
 *
 * This is the Next.js half of the boundary. It is not the security control —
 * the Go API enforces the same permissions on every request, and it is the only
 * thing standing between a caller and the data. What this adds is that an
 * operator who cannot use a page never receives its markup at all, so a page
 * shell never renders and then retracts, and a route is not merely hidden by
 * client state that a curious operator can flip.
 */

const PANEL_BASE = process.env.API_INTERNAL_URL ?? process.env.NEXT_PUBLIC_API_URL ?? "";
const SESSION_COOKIE = "__Host-omniflow_admin";

/**
 * Reads the session using the caller's own cookie.
 *
 * Returns null when the operator is not signed in, and throws only when the API
 * is unreachable — the two cases lead to different screens.
 */
export async function readServerSession(): Promise<PanelSession | null> {
  const store = await cookies();
  const session = store.get(SESSION_COOKIE);
  if (!session?.value) {
    return null;
  }

  const response = await fetch(`${PANEL_BASE}/v1/panel/auth/session`, {
    headers: { cookie: `${SESSION_COOKIE}=${session.value}` },
    // Session state must never be served from a cache: a revoked session would
    // keep appearing valid for as long as the entry lived.
    cache: "no-store",
  });

  if (response.status === 401) {
    return null;
  }
  if (!response.ok) {
    throw new Error(`session lookup failed with ${response.status}`);
  }
  return (await response.json()) as PanelSession;
}

/**
 * Gates a server component on a set of permissions.
 *
 * An unauthenticated visitor is sent to sign in with a `next` hop back. A
 * signed-in operator who lacks the permission is sent to the panel root rather
 * than shown a dead end, and the API would refuse the underlying data anyway.
 */
export async function requirePermissions(
  permissions: string[],
  currentPath: string,
): Promise<PanelSession> {
  const session = await readServerSession();
  if (!session) {
    redirect(`/admin/login?next=${encodeURIComponent(currentPath)}`);
  }
  const held = new Set(session.permissions);
  if (!permissions.every((permission) => held.has(permission))) {
    redirect("/admin?denied=1");
  }
  return session;
}
