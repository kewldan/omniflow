/**
 * The customer panel's bridge to Telegram, through tma.js (@tma.js/sdk).
 *
 * Everything here is a no-op outside a Telegram Mini App, and every call is
 * guarded: a client too old for a feature, or a method Telegram refuses,
 * leaves the panel as it was — a working web page. The SDK talks to Telegram
 * the way Telegram's own script does (`TelegramWebviewProxy` in the apps,
 * `postMessage` to Telegram Web when framed) and loads nothing from outside,
 * so the panel's CSP needs no change for it.
 */
import {
  backButton,
  downloadFile,
  hapticFeedback,
  init,
  isTMA,
  miniApp,
  openLink,
  openTelegramLink,
  retrieveRawInitData,
  swipeBehavior,
  viewport,
} from "@tma.js/sdk";

let started = false;

/** Runs a Telegram call, swallowing what an old or refusing client throws. */
function attempt(call: () => void) {
  try {
    call();
  } catch {
    // An unsupported method or a client that refused it: nothing to do.
  }
}

/**
 * Whether this page was opened by Telegram as a Mini App. tma.js reads the
 * launch parameters from the URL and keeps them for the tab, so the answer
 * holds after the first navigation drops the fragment.
 */
export function inTelegram(): boolean {
  if (typeof window === "undefined") {
    return false;
  }
  try {
    return isTMA();
  } catch {
    return false;
  }
}

/**
 * Starts the SDK and prepares the Mini App: ready, expanded, the viewport's
 * safe areas as CSS variables (`--tg-viewport-*`), and vertical swipes off so
 * scrolling a long page cannot close the panel. Returns false outside
 * Telegram. Safe to call more than once.
 */
export function startTelegram(): boolean {
  if (started) {
    return true;
  }
  if (!inTelegram()) {
    return false;
  }
  try {
    init();
  } catch {
    return false;
  }
  started = true;
  attempt(() => miniApp.mount());
  attempt(() => miniApp.ready());
  attempt(() => {
    if (swipeBehavior.isSupported()) {
      swipeBehavior.mount();
      swipeBehavior.disableVertical();
    }
  });
  attempt(() => {
    if (backButton.isSupported()) {
      backButton.mount();
    }
  });
  attempt(() => {
    viewport
      .mount()
      .then(() => {
        attempt(() => viewport.expand());
        attempt(() => {
          if (!viewport.isCssVarsBound()) {
            viewport.bindCssVars();
          }
        });
      })
      .catch(() => undefined);
  });
  return true;
}

/** Asks Telegram for full screen, where the client supports it. */
export function requestTelegramFullscreen() {
  if (!started) {
    return;
  }
  attempt(() => {
    if (viewport.requestFullscreen.isAvailable()) {
      viewport.requestFullscreen().catch(() => undefined);
    }
  });
}

/**
 * The raw launch data Telegram signed for this Mini App, for the sign-in
 * request; tma.js keeps it for the tab, so it is there after navigation too.
 */
export function telegramInitData(): string | undefined {
  try {
    const raw = retrieveRawInitData();
    return raw && raw.length > 0 ? raw : undefined;
  } catch {
    return undefined;
  }
}

/** Haptic feedback, where Telegram offers it; silent everywhere else. */
export const haptic = {
  /** A tap on a control. */
  tap(style: "light" | "medium" | "heavy" | "rigid" | "soft" = "light") {
    if (started) {
      attempt(() => hapticFeedback.isSupported() && hapticFeedback.impactOccurred(style));
    }
  },
  /** A choice changed: a tab, a switch, an option. */
  selection() {
    if (started) {
      attempt(() => hapticFeedback.isSupported() && hapticFeedback.selectionChanged());
    }
  },
  /** An outcome: something succeeded, failed, or needs attention. */
  notify(type: "success" | "error" | "warning") {
    if (started) {
      attempt(() => hapticFeedback.isSupported() && hapticFeedback.notificationOccurred(type));
    }
  },
};

/**
 * Shows Telegram's own back button and routes its press to `onBack`, or hides
 * it when `onBack` is null. Returns the unsubscribe for the press.
 */
export function setTelegramBackButton(onBack: (() => void) | null): () => void {
  if (!started) {
    return () => undefined;
  }
  let off: () => void = () => undefined;
  attempt(() => {
    if (!backButton.isMounted()) {
      return;
    }
    if (onBack) {
      backButton.show();
      off = backButton.onClick(onBack);
    } else {
      backButton.hide();
    }
  });
  return off;
}

/**
 * Paints Telegram's header, background and bottom bar in the panel's own
 * background colour, so the Mini App reads as one surface. Takes a
 * `#rrggbb` colour.
 */
export function setTelegramChromeColor(color: string) {
  if (!started || !/^#[0-9a-f]{6}$/i.test(color)) {
    return;
  }
  attempt(
    () => miniApp.setHeaderColor.isAvailable() && miniApp.setHeaderColor(color as `#${string}`),
  );
  attempt(() => miniApp.setBgColor.isAvailable() && miniApp.setBgColor(color as `#${string}`));
  attempt(
    () =>
      miniApp.setBottomBarColor.isAvailable() && miniApp.setBottomBarColor(color as `#${string}`),
  );
}

/**
 * Opens a link the way a Mini App should: a t.me link inside Telegram, any
 * other web link in Telegram's browser rather than in place of the panel.
 * Returns false when it did not handle the link.
 */
export function openThroughTelegram(href: string): boolean {
  if (!started) {
    return false;
  }
  let url: URL;
  try {
    url = new URL(href, window.location.href);
  } catch {
    return false;
  }
  if (url.origin === window.location.origin) {
    return false;
  }
  if (url.protocol === "https:" && url.hostname === "t.me") {
    let opened = false;
    attempt(() => {
      openTelegramLink(url.toString());
      opened = true;
    });
    return opened;
  }
  return openInTelegramBrowser(url.toString());
}

/**
 * Opens a web link, this site's own included, in Telegram's browser, leaving
 * the panel where it was: a document read from a dialog the customer has to
 * come back to. Returns false outside Telegram or for a non-web link.
 */
export function openInTelegramBrowser(href: string): boolean {
  if (!started) {
    return false;
  }
  let url: URL;
  try {
    url = new URL(href, window.location.href);
  } catch {
    return false;
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") {
    return false;
  }
  let opened = false;
  attempt(() => {
    openLink(url.toString());
    opened = true;
  });
  return opened;
}

/**
 * Whether Telegram can save a file for the page. Its web view drops a download
 * the page starts itself, so a file has to be handed to Telegram by address.
 */
export function canDownloadThroughTelegram(): boolean {
  if (!started) {
    return false;
  }
  try {
    return downloadFile.isAvailable();
  } catch {
    return false;
  }
}

/**
 * Asks Telegram to download `url` (https) under `fileName`. Telegram shows its
 * own prompt and fetches the address itself, without the page's cookies.
 */
export async function downloadThroughTelegram(url: string, fileName: string): Promise<void> {
  await downloadFile(url, fileName);
}
