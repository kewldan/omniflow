"use client";

import { useLocale, useTranslations } from "next-intl";
import { useCallback } from "react";
import { providerKey } from "@/lib/payment-providers";
import type { PaymentChoice } from "./types";

/** Method keys this build has copy for, shown after the provider's name. */
const METHODS = ["sbp", "card", "sberpay", "crypto", "intl", "applepay", "googlepay", "mastercard"];

/**
 * A payment button's identity: the provider alone, or provider.method when the
 * operator shows one provider's methods as separate buttons.
 */
export function choiceKey(choice: Pick<PaymentChoice, "provider" | "key">): string {
  return choice.key || choice.provider;
}

/** The provider and method a button key names, as the payment routes take them. */
export function splitChoiceKey(key: string): { provider: string; method: string } {
  const [provider, method = ""] = key.split(".", 2);
  return { method, provider };
}

/**
 * What a payment button says and the line under it: the operator's own name
 * and description in the panel's language when they wrote one, otherwise the
 * built-in provider name with the method beside it.
 */
export function useChoiceCopy(): (choice: PaymentChoice) => { label: string; description: string } {
  const translate = useTranslations("account.commerce");
  const locale = useLocale();
  return useCallback(
    (choice: PaymentChoice) => {
      const language = locale.startsWith("ru") ? "ru" : "en";
      const custom = choice.name?.[language] || "";
      let label = custom;
      if (!label) {
        const provider = translate(`checkout.provider.names.${providerKey(choice.provider)}`);
        // A provider's own method is named by the method alone — "SBP", not
        // "Card · SBP": the provider is how it is settled, not what the
        // customer chooses. Its card method keeps the provider's name, which
        // already says card; a method this build has no copy for keeps both.
        if (!choice.method || choice.method === "card") {
          label = provider;
        } else if (METHODS.includes(choice.method)) {
          const method = translate(`checkout.provider.methods.${choice.method}`);
          label = method.charAt(0).toLocaleUpperCase(locale) + method.slice(1);
        } else {
          label = `${provider} · ${choice.method.toUpperCase()}`;
        }
      }
      return { description: choice.description?.[language] || "", label };
    },
    [locale, translate],
  );
}
