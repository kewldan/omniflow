import { readAnalytics } from "@/lib/analytics";

/**
 * Antilopay's file-based Apple Pay domain check: the `apay-tag` value the
 * operator saved under advertising measurement, as plain text. The same value
 * is rendered as a meta tag in every page's head.
 */
export async function GET(): Promise<Response> {
  const analytics = await readAnalytics();
  const tag = analytics.verifications?.find((verification) => verification.name === "apay-tag");
  if (!tag?.content) {
    return new Response("Not found", { status: 404 });
  }
  return new Response(tag.content, {
    headers: {
      "Cache-Control": "public, max-age=300",
      "Content-Type": "text/plain; charset=utf-8",
    },
  });
}
