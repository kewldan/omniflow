"use client";

import { createContext, type ReactNode, useContext, useEffect, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { isTelegramMiniApp, prepareMiniApp } from "@/lib/telegram-miniapp";

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

/**
 * Adapts the customer panel to Telegram when it is opened as a Mini App.
 *
 * Outside Telegram it does nothing and asks for nothing. Inside, it tells
 * Telegram the panel is ready and expanded, asks for full screen when the
 * operator chose it (the bot section's `miniAppFullscreen`), and exposes the
 * lite navigation (`miniAppLite`) to the shell. Full screen draws Telegram's
 * own controls over the top of the page, so the page is pushed below them.
 */
export function MiniAppProvider({ children }: { children: ReactNode }) {
  const [inMiniApp, setInMiniApp] = useState(false);
  useEffect(() => {
    setInMiniApp(isTelegramMiniApp());
  }, []);
  const { data } = useSWR<MiniAppSettings, ApiError>(
    inMiniApp ? "/v1/account/miniapp" : null,
    fetcher,
    { revalidateOnFocus: false },
  );
  const fullscreen = inMiniApp && Boolean(data?.fullscreen);
  const lite = inMiniApp && Boolean(data?.lite);

  useEffect(() => {
    if (inMiniApp) {
      prepareMiniApp(fullscreen);
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

  return (
    <MiniAppContext.Provider value={{ inMiniApp, fullscreen, lite }}>
      {children}
    </MiniAppContext.Provider>
  );
}
