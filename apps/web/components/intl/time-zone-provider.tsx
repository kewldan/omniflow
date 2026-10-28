"use client";

import { NextIntlClientProvider, useLocale, useMessages } from "next-intl";
import { type ReactNode, useSyncExternalStore } from "react";

import { formats } from "@/i18n/formats";

/**
 * The zone every date beneath it is shown in.
 *
 * The server renders in UTC (see `i18n/request.ts`): it has no way to know who
 * is looking. Each panel then narrows that to the person's own zone on the
 * client — the customer's account timezone, which is also what quiet hours are
 * computed in, and the operator's browser zone, which the panel names in its
 * header so nobody has to guess.
 *
 * It re-provides the messages the surrounding provider already holds, by
 * reference, so nothing is fetched or serialised a second time.
 */
export function TimeZoneProvider({
  children,
  timeZone,
}: {
  children: ReactNode;
  timeZone: string;
}) {
  const locale = useLocale();
  const messages = useMessages();
  return (
    <NextIntlClientProvider
      formats={formats}
      locale={locale}
      messages={messages}
      timeZone={validZone(timeZone)}
    >
      {children}
    </NextIntlClientProvider>
  );
}

/** A zone `Intl` accepts, or UTC: a stored value from an older build may not be one. */
function validZone(timeZone: string): string {
  try {
    new Intl.DateTimeFormat("en", { timeZone });
    return timeZone;
  } catch {
    return "UTC";
  }
}

const noSubscription = () => () => {};

/**
 * The browser's own time zone, or UTC during the server render and hydration,
 * so the first client render matches the server's markup and the zone switches
 * in immediately afterwards.
 */
export function useBrowserTimeZone(): string {
  return useSyncExternalStore(
    noSubscription,
    () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
    () => "UTC",
  );
}
