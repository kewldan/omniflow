import { cookies, headers } from "next/headers";
import { getRequestConfig } from "next-intl/server";

import { LOCALE_COOKIE, resolveLocale } from "@/lib/locale";
import { formats } from "./formats";
import { missingMessage } from "./missing-message";

type Catalogue = { [key: string]: string | Catalogue };

/** Lays a partial catalogue over English, key by key. */
function overlay(base: Catalogue, partial: Catalogue): Catalogue {
  const merged: Catalogue = { ...base };
  for (const [key, value] of Object.entries(partial)) {
    const under = merged[key];
    merged[key] =
      typeof value === "object" && typeof under === "object" ? overlay(under, value) : value;
  }
  return merged;
}

/**
 * The catalogue for a language. Russian and English are complete; the partial
 * ones fall back to English for every message they do not translate.
 */
async function loadMessages(locale: string): Promise<Catalogue> {
  const own = (await import(`../messages/${locale}.json`)).default as Catalogue;
  if (locale === "ru" || locale === "en") {
    return own;
  }
  const english = (await import("../messages/en.json")).default as Catalogue;
  return overlay(english, own);
}

export default getRequestConfig(async ({ requestLocale }) => {
  // See lib/locale.ts: without this the Russian catalogue was never served.
  const [requested, cookieStore, headerList] = await Promise.all([
    requestLocale,
    cookies(),
    headers(),
  ]);
  const locale = resolveLocale({
    acceptLanguage: headerList.get("accept-language"),
    cookie: cookieStore.get(LOCALE_COOKIE)?.value,
    requested,
  });

  return {
    locale,
    messages: await loadMessages(locale),
    formats,
    // The server cannot know who is looking, so it renders in UTC — which is
    // also what the API's timestamps and the reports' day boundaries are in.
    // Each panel narrows it to the viewer's own zone on the client; see
    // TimeZoneProvider. Stating it also stops next-intl from falling back to
    // the server's own zone, which made a server-rendered date depend on
    // where the container happened to run.
    timeZone: "UTC",
    // A message nobody wrote renders as a marker rather than as its own key.
    // See i18n/missing-message.ts for why the default is not good enough.
    getMessageFallback: ({ key, namespace }) => missingMessage(namespace, key),
  };
});
