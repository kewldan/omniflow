"use client";

import { useTimeZone, useTranslations } from "next-intl";
import type { ReactNode } from "react";

import { useAccount } from "@/lib/account-session";

import { TimeZoneProvider, useBrowserTimeZone } from "./time-zone-provider";

/**
 * The customer panel shows every moment in the account's own timezone.
 *
 * It is the zone quiet hours are computed in and the one the preferences screen
 * sets, so a delivery window and an order time on the same screen can never
 * disagree. Until the session has loaded, the browser's zone stands in.
 */
export function AccountTimeZone({ children }: { children: ReactNode }) {
  const { session } = useAccount();
  const browser = useBrowserTimeZone();
  return (
    <TimeZoneProvider timeZone={session?.customer.timezone || browser}>{children}</TimeZoneProvider>
  );
}

/**
 * The operator panel shows every moment in the operator's browser zone, and
 * names that zone in its header (see `TimeZoneLabel`). Reports that bucket by
 * day keep UTC and say so on their own pages.
 */
export function OperatorTimeZone({ children }: { children: ReactNode }) {
  return <TimeZoneProvider timeZone={useBrowserTimeZone()}>{children}</TimeZoneProvider>;
}

/** The zone the surrounding panel shows times in, with its current offset. */
export function TimeZoneLabel({ className }: { className?: string }) {
  const translate = useTranslations("common.time");
  const timeZone = useTimeZone() ?? "UTC";
  const offset =
    new Intl.DateTimeFormat("en", { timeZone, timeZoneName: "shortOffset" })
      .formatToParts(new Date())
      .find((part) => part.type === "timeZoneName")?.value ?? "";
  const zone = timeZone === "UTC" ? "UTC" : `${timeZone.replaceAll("_", " ")} (${offset})`;
  return (
    <span className={className} suppressHydrationWarning>
      {translate("shownIn", { zone })}
    </span>
  );
}
