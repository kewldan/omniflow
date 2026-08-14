"use client";

import { ThemeProvider as NextThemeProvider } from "next-themes";
import { createContext, type ReactNode, useContext } from "react";

/**
 * Which modes this installation offers.
 *
 * next-themes has no notion of a restricted set: given `forcedTheme` it simply
 * ignores `setTheme`, which would leave every theme control on both surfaces
 * rendered, pressable, and inert. Publishing the set separately is what lets a
 * toggle disappear instead of lying.
 */
const AllowedThemesContext = createContext<readonly string[]>(["light", "dark"]);

export function useAllowedThemes(): readonly string[] {
  return useContext(AllowedThemesContext);
}

/**
 * The design ships light and dark as equal peers, so the panel follows the
 * operating system by default and remembers an explicit choice after that.
 *
 * An installation may narrow that. `defaultTheme` is what somebody with no
 * stored preference gets, and one allowed mode forces it — in which case there
 * is nothing to remember and nothing to toggle, so next-themes is told to stop
 * following the system as well.
 */
export function ThemeProvider({
  children,
  allowedThemes = ["light", "dark"],
  defaultTheme = "system",
}: {
  children: ReactNode;
  allowedThemes?: readonly string[];
  defaultTheme?: string;
}) {
  const forcedTheme = allowedThemes.length === 1 ? allowedThemes[0] : undefined;
  return (
    <AllowedThemesContext value={allowedThemes}>
      <NextThemeProvider
        attribute="class"
        defaultTheme={defaultTheme}
        disableTransitionOnChange
        enableSystem={!forcedTheme}
        forcedTheme={forcedTheme}
      >
        {children}
      </NextThemeProvider>
    </AllowedThemesContext>
  );
}
