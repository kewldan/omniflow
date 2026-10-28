import { NextIntlClientProvider } from "next-intl";
import { getMessages } from "next-intl/server";
import type { ReactNode } from "react";

/**
 * The top-level catalogue namespaces a client provider can carry.
 *
 * `common` holds the copy every surface shares (error pages, copy buttons,
 * relative time) and `analytics` the consent request. `account` and `admin` are
 * the two panels, and together they are almost the whole catalogue.
 */
export type MessageNamespace = "common" | "analytics" | "account" | "admin";

/**
 * Hands the browser only the parts of the catalogue a route group renders.
 *
 * Every message given to a client provider is serialised into the RSC payload
 * of every page beneath it. At the root that meant the operator panel's copy —
 * well over half the catalogue — shipped with the customer's sign-in screen and
 * checkout, which are opened on a phone, often inside Telegram. Server
 * components are unaffected: `getTranslations` reads the full catalogue on the
 * server whatever the provider carries.
 *
 * Providers do not merge. A nested provider replaces its parent's messages for
 * everything beneath it, so each group lists every namespace its client
 * components read, including the shared ones.
 */
export async function ScopedIntlProvider({
  children,
  namespaces,
}: {
  children: ReactNode;
  namespaces: readonly MessageNamespace[];
}) {
  const all = await getMessages();
  const messages = Object.fromEntries(
    namespaces
      .filter((namespace) => namespace in all)
      .map((namespace) => [namespace, all[namespace]]),
  );
  return <NextIntlClientProvider messages={messages}>{children}</NextIntlClientProvider>;
}
