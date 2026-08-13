import { type NextRequest, NextResponse } from "next/server";

/**
 * Edge gate for the panel, and the fallback route from the browser to the API.
 *
 * The gate only checks that a session cookie is present — it deliberately does
 * not validate it, because middleware runs on every request and a round trip to
 * the API here would put the panel's latency floor on the auth service.
 * Validation and permission checks happen server-side in the route itself and,
 * decisively, in the Go API.
 *
 * What this buys is that a signed-out visitor is redirected before any panel
 * markup is generated, rather than being shown a shell that then redirects.
 */

/**
 * Both spellings of the session cookie.
 *
 * The API names it `__Host-omniflow_admin` when it carries `Secure` and
 * `omniflow_admin` when it does not, because a browser rejects a `__Host-`
 * cookie without `Secure` outright — which is the documented plain-HTTP local
 * stack. The gate accepts either, so the same build works against both.
 */
const SESSION_COOKIES = ["__Host-omniflow_admin", "omniflow_admin"] as const;

// Routes that must stay reachable without a session, or a fresh installation
// could never create its first owner and nobody could ever sign in.
const PUBLIC_PATHS = ["/admin/login", "/admin/setup"];

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

export function middleware(request: NextRequest) {
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

  if (PUBLIC_PATHS.some((path) => pathname === path || pathname.startsWith(`${path}/`))) {
    return NextResponse.next();
  }

  if (SESSION_COOKIES.some((name) => request.cookies.has(name))) {
    return NextResponse.next();
  }

  const login = new URL("/admin/login", request.url);
  login.searchParams.set("next", pathname + search);
  return NextResponse.redirect(login);
}

export const config = {
  // The panel gate, plus the API route that both panels depend on. Customer
  // routes have their own, separate session model and are not gated here.
  matcher: ["/admin/:path*", "/v1/:path*"],
};
