"use client";

import { ThemeProvider as NextThemeProvider } from "next-themes";
import type { ReactNode } from "react";

/**
 * The design ships light and dark as equal peers, so the panel follows the
 * operating system by default and remembers an explicit choice after that.
 */
export function ThemeProvider({ children }: { children: ReactNode }) {
  return (
    <NextThemeProvider
      attribute="class"
      defaultTheme="system"
      disableTransitionOnChange
      enableSystem
    >
      {children}
    </NextThemeProvider>
  );
}
