"use client";

import { UiLabelsProvider } from "@omniflow/ui/ui-labels";
import { useLocale, useTranslations } from "next-intl";
import type { ReactNode } from "react";

/**
 * Hands the shared primitives their few words — the close buttons' and the
 * breadcrumb's accessible names — in the reader's language, and the locale
 * the date field formats in. Without it a Russian screen reader user heard
 * "Close" on every dialog.
 */
export function UiLabelsBridge({ children }: { children: ReactNode }) {
  const translate = useTranslations("common.ui");
  const locale = useLocale();
  return (
    <UiLabelsProvider
      labels={{
        breadcrumb: translate("breadcrumb"),
        close: translate("close"),
        more: translate("more"),
      }}
      locale={locale}
    >
      {children}
    </UiLabelsProvider>
  );
}
