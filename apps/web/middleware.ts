import { type NextRequest, NextResponse } from "next/server";

/**
 * Edge gate for the panel.
 *
 * This only checks that a session cookie is present — it deliberately does not
 * validate it, because middleware runs on every request and a round trip to the
 * API here would put the panel's latency floor on the auth service. Validation
 * and permission checks happen server-side in the route itself and, decisively,
 * in the Go API.
 *
 * What this buys is that a signed-out visitor is redirected before any panel
 * markup is generated, rather than being shown a shell that then redirects.
 */
const SESSION_COOKIE = "__Host-omniflow_admin";

// Routes that must stay reachable without a session, or a fresh installation
// could never create its first owner and nobody could ever sign in.
const PUBLIC_PATHS = ["/admin/login", "/admin/setup"];

export function middleware(request: NextRequest) {
  const { pathname, search } = request.nextUrl;

  if (PUBLIC_PATHS.some((path) => pathname === path || pathname.startsWith(`${path}/`))) {
    return NextResponse.next();
  }

  if (request.cookies.has(SESSION_COOKIE)) {
    return NextResponse.next();
  }

  const login = new URL("/admin/login", request.url);
  login.searchParams.set("next", pathname + search);
  return NextResponse.redirect(login);
}

export const config = {
  // Scoped to the panel: customer routes have their own, separate model.
  matcher: ["/admin/:path*"],
};
