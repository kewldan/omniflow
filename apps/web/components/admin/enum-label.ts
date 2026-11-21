"use client";

import { useLocale, useTranslations } from "next-intl";
import { useCallback } from "react";

/**
 * Words for the codes the API returns: an order's operation and state, a
 * payment's status, a provider, a ledger line's type.
 *
 * The API speaks in stable codes and the panel used to print them as they
 * came, so an operator read `fulfilled` and `requires_action` next to Russian
 * headings. Every code has copy under `admin.enums.<group>` in each catalogue;
 * one this build has no copy for still shows, as its code with spaces, rather
 * than disappearing.
 */
export type EnumGroup =
  | "aiLimitScope"
  | "aiProviderKind"
  | "backupKind"
  | "backupStatus"
  | "customerEvent"
  | "deliveryStatus"
  | "fulfillmentOperation"
  | "fulfillmentStatus"
  | "health"
  | "intentStatus"
  | "ledgerType"
  | "matchStatus"
  | "orderOperation"
  | "orderState"
  | "provider"
  | "refundStatus"
  | "restoreStatus"
  | "subjectKind"
  | "subscriptionStatus"
  | "suppressionReason"
  | "ticketStatus"
  | "webhookStatus";

/** A message key cannot hold a dot: `wallet.adjusted` is read as `wallet_adjusted`. */
function messageKey(code: string): string {
  return code.replace(/[^a-zA-Z0-9_]/g, "_");
}

export function useEnumLabel() {
  const translate = useTranslations("admin.enums");
  return useCallback(
    (group: EnumGroup, code: string | null | undefined): string => {
      if (!code) {
        return "—";
      }
      const key = `${group}.${messageKey(code)}`;
      return translate.has(key) ? translate(key as never) : code.replaceAll("_", " ");
    },
    [translate],
  );
}

/**
 * The country whose flag stands for a language. A language is not a country,
 * so this is the conventional pick, and a tag with a region (`pt-BR`) uses
 * that region instead.
 */
const LANGUAGE_FLAGS: Record<string, string> = {
  ar: "SA",
  az: "AZ",
  be: "BY",
  bg: "BG",
  cs: "CZ",
  de: "DE",
  el: "GR",
  en: "GB",
  es: "ES",
  et: "EE",
  fa: "IR",
  fi: "FI",
  fr: "FR",
  he: "IL",
  hi: "IN",
  hu: "HU",
  hy: "AM",
  id: "ID",
  it: "IT",
  ja: "JP",
  ka: "GE",
  kk: "KZ",
  ko: "KR",
  ky: "KG",
  lt: "LT",
  lv: "LV",
  nl: "NL",
  pl: "PL",
  pt: "PT",
  ro: "RO",
  ru: "RU",
  sr: "RS",
  sv: "SE",
  tg: "TJ",
  tr: "TR",
  uk: "UA",
  uz: "UZ",
  vi: "VN",
  zh: "CN",
};

function flagOf(country: string): string {
  return String.fromCodePoint(...[...country].map((letter) => 0x1f1e6 + letter.charCodeAt(0) - 65));
}

/** A language code as a flag and the language's name: `ru` reads «🇷🇺 Русский». */
export function useLanguageLabel() {
  const locale = useLocale();
  return useCallback(
    (code: string | null | undefined): string => {
      if (!code) {
        return "—";
      }
      const [language = "", region] = code.toLowerCase().split(/[-_]/);
      const country =
        region && /^[a-z]{2}$/.test(region) ? region.toUpperCase() : LANGUAGE_FLAGS[language];
      let name = code;
      try {
        name = new Intl.DisplayNames([locale], { type: "language" }).of(code) ?? code;
      } catch {
        // A tag the browser cannot name keeps its code.
      }
      name = name.charAt(0).toLocaleUpperCase(locale) + name.slice(1);
      return country ? `${flagOf(country)} ${name}` : name;
    },
    [locale],
  );
}
