/**
 * What a missing translation renders as.
 *
 * It lives in a module of its own so the browser suite can assert on the same
 * definition the application renders, without importing `i18n/request.ts` and
 * dragging `next-intl/server` into a Playwright process that has no Next.js
 * request around it.
 *
 * The marker is deliberately not English and not dotted. next-intl's default is
 * to render the key itself, which fails twice: it looks like copy nobody wrote
 * — `admin.navigation.items.offers` shipped in a sidebar that way — and it is
 * indistinguishable from data, because the audit log stores action names such
 * as `admin.bootstrap.completed`. A gate hunting for a dotted key matched real
 * audit rows and failed a page that was fully translated.
 */
export const MISSING_MESSAGE_MARKER = "⟦missing:";

/** Renders one missing message. */
export function missingMessage(namespace: string | undefined, key: string): string {
  return `${MISSING_MESSAGE_MARKER}${[namespace, key].filter(Boolean).join(".")}⟧`;
}
