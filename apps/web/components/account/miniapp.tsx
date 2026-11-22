"use client";

import { usePathname, useRouter } from "next/navigation";
import { createContext, type ReactNode, useContext, useEffect, useRef, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { isTelegramMiniApp } from "@/lib/telegram-miniapp";
import {
  haptic,
  inTelegram,
  openThroughTelegram,
  requestTelegramFullscreen,
  setTelegramBackButton,
  setTelegramChromeColor,
  startTelegram,
} from "@/lib/tma";

type MiniAppSettings = { fullscreen: boolean; lite: boolean };
type MiniAppState = { inMiniApp: boolean; fullscreen: boolean; lite: boolean };

const MiniAppContext = createContext<MiniAppState>({
  inMiniApp: false,
  fullscreen: false,
  lite: false,
});

/** How the customer panel is being shown: in a browser, or inside Telegram. */
export function useMiniApp(): MiniAppState {
  return useContext(MiniAppContext);
}

/** Screens that are a start rather than a step: Telegram's back button hides there. */
const ROOTS = new Set(["/account", "/account/sign-in"]);

/** Controls whose press is a choice rather than an action. */
const SELECTION_ROLES = new Set(["tab", "radio", "switch", "checkbox", "option", "menuitemradio"]);

/**
 * Adapts the customer panel to Telegram when it is opened as a Mini App.
 *
 * Outside Telegram it does nothing and asks for nothing. Inside, through
 * tma.js, it tells Telegram the panel is ready and expanded, keeps a swipe
 * down from closing it, asks for full screen when the operator chose it (the
 * bot section's `miniAppFullscreen`), shows Telegram's back button on every
 * screen past the first, answers presses with Telegram's haptics, paints
 * Telegram's header in the panel's background, and opens outside links
 * through Telegram. The lite navigation (`miniAppLite`) is exposed to the
 * shell. Full screen draws Telegram's own controls over the top of the page,
 * so the page is pushed below them by the safe areas Telegram reports.
 */
export function MiniAppProvider({ children }: { children: ReactNode }) {
  const [inMiniApp, setInMiniApp] = useState(false);
  useEffect(() => {
    const started = startTelegram();
    setInMiniApp(started || inTelegram() || isTelegramMiniApp());
  }, []);
  const { data } = useSWR<MiniAppSettings, ApiError>(
    inMiniApp ? "/v1/account/miniapp" : null,
    fetcher,
    { revalidateOnFocus: false },
  );
  const fullscreen = inMiniApp && Boolean(data?.fullscreen);
  const lite = inMiniApp && Boolean(data?.lite);

  useEffect(() => {
    if (inMiniApp && fullscreen) {
      requestTelegramFullscreen();
    }
  }, [inMiniApp, fullscreen]);

  useEffect(() => {
    const root = document.documentElement;
    if (!inMiniApp) {
      return;
    }
    root.dataset.miniapp = fullscreen ? "fullscreen" : "embedded";
    return () => {
      delete root.dataset.miniapp;
    };
  }, [inMiniApp, fullscreen]);

  // Telegram's back button on every screen past the first. The first screen
  // the panel opened on has nothing in the tab's history to go back to, so a
  // press there goes to the account's front page instead.
  const pathname = usePathname();
  const router = useRouter();
  const steps = useRef(0);
  const previous = useRef<string | null>(null);
  useEffect(() => {
    if (!inMiniApp) {
      return;
    }
    if (previous.current !== null && previous.current !== pathname) {
      steps.current += 1;
    }
    previous.current = pathname;
    if (ROOTS.has(pathname)) {
      return setTelegramBackButton(null);
    }
    return setTelegramBackButton(() => {
      haptic.tap();
      if (steps.current > 0) {
        steps.current -= 2;
        router.back();
      } else {
        router.push("/account");
      }
    });
  }, [inMiniApp, pathname, router]);

  // Haptics for presses and outcomes, and Telegram's own handling of links
  // that leave the panel.
  useEffect(() => {
    if (!inMiniApp) {
      return;
    }
    const onClick = (event: MouseEvent) => {
      const target = event.target instanceof Element ? event.target : null;
      const link = target?.closest("a[href]");
      if (link instanceof HTMLAnchorElement && !event.defaultPrevented) {
        if (openThroughTelegram(link.href)) {
          event.preventDefault();
          haptic.tap();
          return;
        }
      }
      const control = target?.closest(
        "button, a[href], [role=button], [role=tab], [role=radio], [role=switch], [role=checkbox], [role=option], [role=menuitem], [role=menuitemradio]",
      );
      if (
        !control ||
        control.hasAttribute("disabled") ||
        control.getAttribute("aria-disabled") === "true"
      ) {
        return;
      }
      const role = control.getAttribute("role");
      if (role && SELECTION_ROLES.has(role)) {
        haptic.selection();
      } else {
        haptic.tap();
      }
    };
    document.addEventListener("click", onClick, true);

    // A toast is the panel saying how something went; Telegram's
    // notification haptic says it too.
    const observer = new MutationObserver((records) => {
      for (const record of records) {
        for (const node of record.addedNodes) {
          if (!(node instanceof HTMLElement)) {
            continue;
          }
          const toast = node.matches("[data-sonner-toast]")
            ? node
            : node.querySelector("[data-sonner-toast]");
          const type = toast?.getAttribute("data-type");
          if (type === "success" || type === "error" || type === "warning") {
            haptic.notify(type);
          }
        }
      }
    });
    observer.observe(document.body, { childList: true, subtree: true });
    return () => {
      document.removeEventListener("click", onClick, true);
      observer.disconnect();
    };
  }, [inMiniApp]);

  // Telegram's header and background take the panel's background colour, and
  // follow it when the theme changes.
  useEffect(() => {
    if (!inMiniApp) {
      return;
    }
    const paint = () =>
      setTelegramChromeColor(cssColorToHex(getComputedStyle(document.body).backgroundColor));
    paint();
    const observer = new MutationObserver(paint);
    observer.observe(document.documentElement, {
      attributeFilter: ["class", "style"],
      attributes: true,
    });
    return () => observer.disconnect();
  }, [inMiniApp]);

  return (
    <MiniAppContext.Provider value={{ inMiniApp, fullscreen, lite }}>
      {children}
    </MiniAppContext.Provider>
  );
}

/** `rgb(9, 9, 11)` → `#09090b`; anything else (a transparent body) → "". */
function cssColorToHex(color: string): string {
  const match = /^rgba?\((\d+),\s*(\d+),\s*(\d+)(?:,\s*([\d.]+))?\)$/.exec(color.trim());
  if (!match || (match[4] !== undefined && Number(match[4]) === 0)) {
    return "";
  }
  return `#${[match[1], match[2], match[3]]
    .map((part) => Number(part).toString(16).padStart(2, "0"))
    .join("")}`;
}
