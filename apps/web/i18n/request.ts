import { getRequestConfig } from "next-intl/server";

const supportedLocales = ["ru", "en"] as const;

export default getRequestConfig(async ({ requestLocale }) => {
  const requested = await requestLocale;
  const locale = supportedLocales.includes(requested as (typeof supportedLocales)[number])
    ? (requested as (typeof supportedLocales)[number])
    : "en";

  return {
    locale,
    messages: (await import(`../messages/${locale}.json`)).default,
  };
});
