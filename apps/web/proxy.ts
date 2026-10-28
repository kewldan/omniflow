import { type NextRequest, NextResponse } from "next/server";

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
const ADMIN_PUBLIC_PATHS = ["/admin/login", "/admin/setup"];

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

  const redirect = signedOutRedirect(pathname, search, {
    account: hasCookie(request, ACCOUNT_SESSION_COOKIES),
    admin: hasCookie(request, ADMIN_SESSION_COOKIES),
  });
  if (redirect) {
    return NextResponse.redirect(new URL(redirect, request.url));
  }
  return NextResponse.next();
}

export const config = {
  // Both panel gates, plus the API route both panels depend on. Each panel has
  // its own session cookie, so a customer cookie never opens /admin and an
  // operator cookie never opens /account.
  matcher: ["/admin/:path*", "/account/:path*", "/v1/:path*"],
};
