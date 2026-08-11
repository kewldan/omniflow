import { Toaster } from "@omniflow/ui/toast";
import { GeistMono } from "geist/font/mono";
import { GeistSans } from "geist/font/sans";
import type { Metadata } from "next";
import { NextIntlClientProvider } from "next-intl";
import { getLocale, getMessages } from "next-intl/server";
import type { ReactNode } from "react";

import { ThemeProvider } from "@/components/theme-provider";
import "./globals.css";

export const metadata: Metadata = {
  title: { default: "Omniflow", template: "%s · Omniflow" },
  description: "VPN subscriptions, customer self-service, and service operations.",
};

export default async function RootLayout({ children }: Readonly<{ children: ReactNode }>) {
  const locale = await getLocale();
  const messages = await getMessages();
  return (
    /*
     * `suppressHydrationWarning` is required on <html> because next-themes
     * writes the resolved theme class before React hydrates, so the server and
     * client markup differ by design on this element only.
     */
    <html
      className={`${GeistSans.variable} ${GeistMono.variable}`}
      lang={locale}
      suppressHydrationWarning
    >
      <body>
        <ThemeProvider>
          <NextIntlClientProvider messages={messages}>
            {children}
            <Toaster />
          </NextIntlClientProvider>
        </ThemeProvider>
      </body>
    </html>
  );
}
