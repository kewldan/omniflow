import { isAppLink } from "@/lib/open-app";

/**
 * The previous bot's application opener, kept for the links customers still
 * hold: `/miniapp/redirect.html?url=<app link>&lang=…` from its buttons and
 * subscription pages. Telegram cannot open `incy://` or `happ://` from a
 * button, so those links went through this page.
 *
 * It answers with a redirect to this panel's own opener, `/open#<link>`, which
 * follows only an application scheme — never a web address or `javascript:` —
 * so the old address cannot become an open redirect. The link moves into the
 * fragment, which the browser does not send on.
 */
export function GET(request: Request): Response {
  const link = new URL(request.url).searchParams.get("url") ?? "";
  const target = isAppLink(link) ? `/open#${encodeURIComponent(link)}` : "/open";
  return new Response(null, {
    headers: { "Cache-Control": "no-store", Location: target, "Referrer-Policy": "no-referrer" },
    status: 302,
  });
}
