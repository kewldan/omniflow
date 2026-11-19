/**
 * Which language a request is rendered in.
 *
 * Nothing used to decide this. next-intl reads the locale from a header its own
 * routing middleware sets, this application does not use that routing, and so
 * every page in both panels rendered from the English catalogue — the Russian
 * one was complete and never served, and a customer's saved language changed
 * only what the bot wrote.
 *
 * The order is: a locale next-intl was handed explicitly, then the language
 * cookie, then the browser's `Accept-Language`, then English. The cookie is
 * written when someone picks a language and whenever a signed-in session
 * carries a saved preference, so the account's choice follows it between
 * browsers while a visitor who has not signed in still gets their browser's
 * language. It holds a language code and nothing else.
 */

export const LOCALES = ["ru", "en"] as const;
export type Locale = (typeof LOCALES)[number];
export const DEFAULT_LOCALE: Locale = "en";

/** The cookie the chosen language is kept in. Not HttpOnly: the panels write it. */
export const LOCALE_COOKIE = "omniflow_locale";

/** A year: a language choice is not something to ask for again next week. */
const COOKIE_MAX_AGE_SECONDS = 60 * 60 * 24 * 365;

export function isLocale(value: unknown): value is Locale {
  return typeof value === "string" && (LOCALES as readonly string[]).includes(value);
}

/**
 * The supported language a browser ranks highest, or null for none.
 *
 * `ru-RU,ru;q=0.9,en;q=0.8` is `ru`; a language with `q=0` is refused rather
 * than chosen; ties keep the browser's own order.
 */
export function negotiateLocale(acceptLanguage: string | null | undefined): Locale | null {
  if (!acceptLanguage) {
    return null;
  }
  const ranked = acceptLanguage
    .split(",")
    .map((entry, position) => {
      const [tag = "", ...parameters] = entry.trim().split(";");
      const quality = parameters
        .map((parameter) => parameter.trim())
        .find((parameter) => parameter.startsWith("q="));
      const weight = quality ? Number.parseFloat(quality.slice(2)) : 1;
      return {
        language: tag.trim().toLowerCase().split("-")[0] ?? "",
        position,
        weight: Number.isFinite(weight) ? weight : 0,
      };
    })
    .filter((entry) => entry.weight > 0)
    .sort((left, right) => right.weight - left.weight || left.position - right.position);
  return ranked.map((entry) => entry.language).find(isLocale) ?? null;
}

export function resolveLocale({
  acceptLanguage,
  cookie,
  requested,
}: {
  acceptLanguage?: string | null;
  cookie?: string | null;
  requested?: string | null;
}): Locale {
  if (isLocale(requested)) {
    return requested;
  }
  if (isLocale(cookie)) {
    return cookie;
  }
  return negotiateLocale(acceptLanguage) ?? DEFAULT_LOCALE;
}

/** The `document.cookie` assignment that records a language choice. */
export function localeCookie(locale: Locale, secure = false): string {
  return [
    `${LOCALE_COOKIE}=${locale}`,
    "Path=/",
    `Max-Age=${COOKIE_MAX_AGE_SECONDS}`,
    "SameSite=Lax",
    ...(secure ? ["Secure"] : []),
  ].join("; ");
}
