import { type NextRequest, NextResponse } from "next/server";

import { buildCsp, createNonce, cspSurface } from "@/lib/csp";
import { SIGN_IN, signInPath } from "@/lib/sign-in-path";

/**
 * Edge gate for both panels, and the fallback route from the browser to the API.
 *
 * Next.js 16 calls this file `proxy.ts` (it was `middleware.ts`); the function
 * is exported as `proxy`.
 *
 * The gate only checks that a session cookie is present — it deliberately does
 * not validate it, because the proxy runs on every request and a round trip to
 * the API here would put the panel's latency floor on the auth service.
 * Validation and permission checks happen server-side in the route itself and,
 * decisively, in the Go API.
 *
 * What this buys is that a signed-out visitor is redirected before any panel
 * markup is generated, rather than being shown a shell that then redirects.
 */

/**
 * Both spellings of each session cookie.
 *
 * The API names a session cookie `__Host-<base>` when it carries `Secure` and
 * `<base>` when it does not, because a browser rejects a `__Host-` cookie
 * without `Secure` outright — which is the documented plain-HTTP local stack.
 * The gate accepts either, so the same build works against both. See
 * `internal/httpapi/cookiename.go`.
 */
const ADMIN_SESSION_COOKIES = ["__Host-omniflow_admin", "omniflow_admin"] as const;
const ACCOUNT_SESSION_COOKIES = ["__Host-omniflow_account", "omniflow_account"] as const;

// Operator routes that must stay reachable without a session, or a fresh
// installation could never create its first owner and nobody could sign in.
// The invitation screen is how an operator without an account gets one.
const ADMIN_PUBLIC_PATHS = ["/admin/login", "/admin/setup", "/admin/invite"];

function within(pathname: string, base: string): boolean {
  return pathname === base || pathname.startsWith(`${base}/`);
}

function hasCookie(request: NextRequest, names: readonly string[]): boolean {
  return names.some((name) => Boolean(request.cookies.get(name)?.value));
}

/**
 * Where `/v1` goes when nothing in front of the stack routes it.
 *
 * Both panels call the API same-origin: the shared browser client keeps a
 * module-level base URL of `""` and nothing calls `setBaseUrl`, so the browser
 * asks its own origin for `/v1/panel/...` and `/v1/account/...`. That is the
 * design, and it is what keeps the session cookie first-party and `SameSite=Lax`
 * workable without CORS — but it only holds if something actually serves `/v1`
 * on the web origin.
 *
 * In production the reverse proxy does it, and the examples under
 * `deploy/proxies` route `/v1` on the web host to the API for exactly that
 * reason; there the request never reaches Next.js and this code is inert. A
 * stack with nothing in front — the compose quickstart, with the web app on
 * :3000 and the API on :8080, and `bun run dev:web` — has nothing playing that
 * part, and without this every call from the browser lands on Next.js, which has
 * no `/v1` route and answers 404. The panel renders and then fails on its first
 * request.
 *
 * It is read at request time rather than through `next.config.ts` rewrites,
 * which are evaluated at build time and baked into the routes manifest: a
 * published image has to be pointable at a different API when it is deployed.
 */
function apiOrigin(): string {
  const configured = process.env.API_INTERNAL_URL ?? process.env.NEXT_PUBLIC_API_URL ?? "";
  return configured.replace(/\/$/, "");
}

/**
 * The redirect a signed-out visitor gets, or null when the route may render.
 *
 * Exported for the unit test; the proxy is the only caller.
 */
export function signedOutRedirect(
  pathname: string,
  search: string,
  has: { admin: boolean; account: boolean },
): string | null {
  if (within(pathname, "/admin")) {
    if (ADMIN_PUBLIC_PATHS.some((path) => within(pathname, path)) || has.admin) {
      return null;
    }
    return `/admin/login?${new URLSearchParams({ next: pathname + search })}`;
  }
  // The customer panel. The sign-in screens, the magic-link landing among
  // them, are what a signed-out customer is sent to, so they stay open.
  if (within(pathname, "/account")) {
    if (within(pathname, SIGN_IN) || has.account) {
      return null;
    }
    return signInPath(pathname + search);
  }
  return null;
}

/**
 * Old cabinet addresses and where they live now.
 *
 * A migrated installation inherits links to the previous bot's cabinet — in
 * e-mails, pinned messages, and bookmarks — on the same host. Each is a key
 * matched as the whole path or a path prefix; the target is a route of the
 * customer panel. The query string is carried, so `/login?ref=CODE` keeps
 * its code.
 */
const LEGACY_PATHS: Record<string, string> = {
  "/login": "/account/sign-in",
  "/auth": "/account/sign-in",
  "/register": "/account/sign-in",
  "/subscription": "/account/subscriptions",
  "/subscriptions": "/account/subscriptions",
  "/referral": "/account/referrals",
  "/referrals": "/account/referrals",
  "/balance": "/account/wallet",
  "/wallet": "/account/wallet",
  "/support": "/account/support",
  "/profile": "/account/profile",
  "/settings": "/account/preferences",
  "/devices": "/account/devices",
  "/buy": "/account/store",
};

/**
 * The first path segments Omniflow itself serves. A path outside them is
 * nothing of Omniflow's; on a host that took over a previous cabinet it is an
 * old address, and LEGACY_PATH_FALLBACK can send it to the account rather
 * than to a 404. Off by default: elsewhere an unknown address is a 404.
 */
const OWN_SEGMENTS = new Set([
  "",
  "account",
  "admin",
  "guest",
  "open",
  "pages",
  "status",
  "unsubscribe",
  "v1",
  "_next",
  ".well-known",
]);

function legacyTable(): Record<string, string> {
  const configured = process.env.LEGACY_PATH_REDIRECTS;
  if (!configured) {
    return LEGACY_PATHS;
  }
  try {
    const parsed = JSON.parse(configured) as unknown;
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      return { ...LEGACY_PATHS, ...(parsed as Record<string, string>) };
    }
  } catch {
    // A malformed table is ignored rather than taking every page down.
  }
  return LEGACY_PATHS;
}

/** Only a path on this host may be a target: never another origin. */
function localTarget(target: unknown): target is string {
  return (
    typeof target === "string" &&
    target.startsWith("/") &&
    !target.startsWith("//") &&
    !target.includes("\\")
  );
}

/**
 * Where an old cabinet address goes, or null for a path Omniflow serves.
 *
 * Exported for the unit test; the proxy is the only caller. `table` and
 * `fallback` default to the built-in table merged with LEGACY_PATH_REDIRECTS,
 * and to LEGACY_PATH_FALLBACK (unset or `off` keeps the 404).
 */
export function legacyRedirect(
  pathname: string,
  search: string,
  table: Record<string, string> = legacyTable(),
  fallback: string = process.env.LEGACY_PATH_FALLBACK ?? "off",
): string | null {
  const segments = pathname.split("/");
  const first = segments[1] ?? "";
  // The previous cabinet's gift links carried the code in the path; the
  // wallet's redeem form reads it from the query.
  if (
    (first === "buy" && segments[2] === "gift" && segments[3]) ||
    (first === "gift" && segments[2])
  ) {
    const code = first === "gift" ? segments[2] : segments[3];
    const query = new URLSearchParams(search);
    query.set("code", decodeURIComponent(code ?? ""));
    return `/account/wallet?${query}`;
  }
  const keys = Object.keys(table).sort((left, right) => right.length - left.length);
  for (const key of keys) {
    if (within(pathname, key) && localTarget(table[key])) {
      return table[key] + search;
    }
  }
  if (
    OWN_SEGMENTS.has(first) ||
    pathname.includes(".") ||
    fallback === "off" ||
    !localTarget(fallback)
  ) {
    return null;
  }
  return fallback + search;
}

export function proxy(request: NextRequest) {
  const { pathname, search } = request.nextUrl;

  if (pathname.startsWith("/v1/")) {
    const origin = apiOrigin();
    if (!origin) {
      // Better a named failure than a 404 that reads as a missing page: this
      // one says which variable is unset and who is meant to set it.
      return NextResponse.json(
        {
          type: "https://omniflow.dev/problems/api-unreachable",
          title: "The API is not routed",
          status: 502,
          detail:
            "Set API_INTERNAL_URL on the web service, or route /v1 to the API " +
            "in the reverse proxy in front of this application.",
        },
        { status: 502, headers: { "content-type": "application/problem+json" } },
      );
    }
    return NextResponse.rewrite(new URL(`${origin}${pathname}${search}`));
  }

  const legacy = legacyRedirect(pathname, search);
  if (legacy) {
    return NextResponse.redirect(new URL(legacy, request.url));
  }

  const redirect = signedOutRedirect(pathname, search, {
    account: hasCookie(request, ACCOUNT_SESSION_COOKIES),
    admin: hasCookie(request, ADMIN_SESSION_COOKIES),
  });
  if (redirect) {
    return NextResponse.redirect(new URL(redirect, request.url));
  }

  // Every page gets a fresh nonce and the policy for its surface. The policy
  // goes on the request too: that is where Next.js reads the nonce to stamp it
  // on its own scripts, and where the root layout reads it for the theme
  // script and the counters. See lib/csp.ts.
  const nonce = createNonce();
  const policy = buildCsp({
    dev: process.env.NODE_ENV === "development",
    nonce,
    surface: cspSurface(pathname),
  });
  const headers = new Headers(request.headers);
  headers.set("x-nonce", nonce);
  headers.set("content-security-policy", policy);
  const response = NextResponse.next({ request: { headers } });
  response.headers.set("content-security-policy", policy);
  return response;
}

export const config = {
  // The API route, and every page for the policy; the panel gates are decided
  // inside. Build output is skipped: it is not a document a policy applies to.
  // Each panel has its own session cookie, so a customer cookie never opens
  // /admin and an operator cookie never opens /account.
  matcher: ["/v1/:path*", "/((?!_next/static|_next/image|favicon\\.ico|v1/).*)"],
};
