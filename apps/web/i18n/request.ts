import { getRequestConfig } from "next-intl/server";

import { missingMessage } from "./missing-message";

const supportedLocales = ["ru", "en"] as const;

export default getRequestConfig(async ({ requestLocale }) => {
  const requested = await requestLocale;
  const locale = supportedLocales.includes(requested as (typeof supportedLocales)[number])
    ? (requested as (typeof supportedLocales)[number])
    : "en";

  return {
    locale,
    messages: (await import(`../messages/${locale}.json`)).default,
    // A message nobody wrote renders as a marker rather than as its own key.
    // See i18n/missing-message.ts for why the default is not good enough.
    getMessageFallback: ({ key, namespace }) => missingMessage(namespace, key),
  };
});
