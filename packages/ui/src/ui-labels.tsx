"use client";

import { createContext, type ReactNode, useContext } from "react";

/**
 * The few words the primitives themselves speak — almost all of it to screen
 * readers: the dialog's and sheet's close button, the breadcrumb landmark, and
 * the breadcrumb's "more" marker.
 *
 * The library has no catalogue of its own and must not depend on the
 * application's, so the application hands it these words once, at the root, in
 * the reader's language. Without a provider the English defaults apply, which
 * is what every one of these primitives used to say whatever the locale.
 */
export type UiLabels = {
  breadcrumb: string;
  close: string;
  more: string;
};

const ENGLISH: UiLabels = { breadcrumb: "Breadcrumb", close: "Close", more: "More" };

type UiLocale = { labels: UiLabels; locale?: string };

const UiLabelsContext = createContext<UiLocale>({ labels: ENGLISH });

export function UiLabelsProvider({
  children,
  labels,
  locale,
}: {
  children: ReactNode;
  labels: Partial<UiLabels>;
  /** The BCP 47 locale the primitives format dates in. */
  locale?: string;
}) {
  return (
    <UiLabelsContext value={{ labels: { ...ENGLISH, ...labels }, locale }}>
      {children}
    </UiLabelsContext>
  );
}

export function useUiLabels(): UiLabels {
  return useContext(UiLabelsContext).labels;
}

export function useUiLocale(): string | undefined {
  return useContext(UiLabelsContext).locale;
}
