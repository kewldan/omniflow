"use client";

import { Button } from "@omniflow/ui/button";
import { Languages } from "lucide-react";
import { useRouter } from "next/navigation";
import { useLocale, useTranslations } from "next-intl";
import { useCallback, useEffect, useRef } from "react";

import { isLocale, type Locale, localeCookie } from "@/lib/locale";

/**
 * Records a language for this browser and re-renders the page in it.
 *
 * The page is rendered on the server from the language cookie (see
 * lib/locale.ts), so writing the cookie and refreshing is the whole switch; no
 * client state has to be kept in step with it.
 */
export function useSwitchLocale(): (locale: Locale) => void {
  const router = useRouter();
  return useCallback(
    (locale: Locale) => {
      // biome-ignore lint/suspicious/noDocumentCookie: a plain language code the server reads on the next render; the Cookie Store API is not in every browser a customer uses.
      document.cookie = localeCookie(locale, window.location.protocol === "https:");
      router.refresh();
    },
    [router],
  );
}

/**
 * Brings the rendered language in line with the one an account has saved.
 *
 * Rendered by both panel shells with the signed-in account's preference, so a
 * language chosen on one device is the language of every other one after
 * sign-in. It switches at most once per mount: if the cookie cannot be written
 * (a browser refusing it), the page stays usable in the language it has.
 */
export function LocaleSync({ preferred }: { preferred: string | null | undefined }) {
  const locale = useLocale();
  const switchLocale = useSwitchLocale();
  const attempted = useRef(false);

  useEffect(() => {
    if (attempted.current || !isLocale(preferred) || preferred === locale) {
      return;
    }
    attempted.current = true;
    switchLocale(preferred);
  }, [locale, preferred, switchLocale]);

  return null;
}

/**
 * A one-press switch to the other language, for screens shown before sign-in.
 *
 * It names the language it switches to, in that language, because someone who
 * cannot read the current one is exactly who needs to find it.
 */
export function LocaleSwitch({ className }: { className?: string }) {
  const locale = useLocale();
  const translate = useTranslations("common.language");
  const switchLocale = useSwitchLocale();
  const other: Locale = locale === "ru" ? "en" : "ru";

  return (
    <Button
      aria-label={translate("switchTo", { language: translate(other) })}
      className={className}
      lang={other}
      onClick={() => switchLocale(other)}
      size="sm"
      type="button"
      variant="ghost"
    >
      <Languages aria-hidden />
      {translate(other)}
    </Button>
  );
}
