/**
 * The customer panel inside Telegram, without Telegram's own script.
 *
 * The panel does not load `telegram-web-app.js` (it would be a third-party
 * script in every customer page, and the CSP keeps those out). What it needs
 * from the client is small — expand, go full screen — and both are messages of
 * the Mini Apps protocol, which this posts the same way that script does:
 * through `TelegramWebviewProxy` in the mobile and desktop apps, and through
 * `postMessage` to Telegram Web when the panel is framed by it.
 */

/** sessionStorage key remembering that this tab was opened as a Mini App. */
const MINI_APP_FLAG = "omniflow.miniapp";

/** The Telegram Web origin a framed Mini App talks to. */
const TELEGRAM_WEB_ORIGIN = "https://web.telegram.org";

type TelegramWindow = Window & {
  TelegramWebviewProxy?: { postEvent: (eventType: string, eventData: string) => void };
  Telegram?: { WebApp?: { initData?: string } };
};

/**
 * Whether a launch fragment carries Telegram's Mini App parameters. Telegram
 * appends `tgWebAppData`, `tgWebAppPlatform`, and `tgWebAppVersion` to the
 * URL fragment of every Mini App it opens.
 */
export function isMiniAppFragment(hash: string): boolean {
  const parameters = new URLSearchParams(hash.replace(/^#/, ""));
  return parameters.has("tgWebAppData") || parameters.has("tgWebAppPlatform");
}

/**
 * Whether this tab is a Telegram Mini App. The launch fragment is gone after
 * the first navigation, so a positive answer is remembered for the tab.
 */
export function isTelegramMiniApp(): boolean {
  if (typeof window === "undefined") {
    return false;
  }
  const telegram = window as TelegramWindow;
  const detected =
    isMiniAppFragment(window.location.hash) ||
    telegram.TelegramWebviewProxy !== undefined ||
    Boolean(telegram.Telegram?.WebApp?.initData);
  try {
    if (detected) {
      window.sessionStorage.setItem(MINI_APP_FLAG, "1");
      return true;
    }
    return window.sessionStorage.getItem(MINI_APP_FLAG) === "1";
  } catch {
    return detected;
  }
}

/**
 * Posts one Mini Apps protocol event. A client that does not know the event —
 * full screen arrived with Bot API 8.0 — ignores it, so nothing here checks
 * versions.
 */
export function postMiniAppEvent(eventType: string, eventData: Record<string, unknown> = {}) {
  if (typeof window === "undefined") {
    return;
  }
  const telegram = window as TelegramWindow;
  try {
    if (telegram.TelegramWebviewProxy) {
      telegram.TelegramWebviewProxy.postEvent(eventType, JSON.stringify(eventData));
      return;
    }
    if (window.parent !== window) {
      window.parent.postMessage(JSON.stringify({ eventType, eventData }), TELEGRAM_WEB_ORIGIN);
    }
  } catch {
    // A client that refuses the message leaves the panel as it opened, which
    // is a working panel.
  }
}

/**
 * Tells Telegram the panel is ready and asks for the space it was configured
 * to take: always expanded, and full screen when the operator chose it.
 */
export function prepareMiniApp(fullscreen: boolean) {
  postMiniAppEvent("web_app_ready");
  postMiniAppEvent("web_app_expand");
  if (fullscreen) {
    postMiniAppEvent("web_app_request_fullscreen");
  }
}

/** Who opened the Mini App, as Telegram's launch data describes them. */
export type MiniAppUser = {
  /** First and last name, or the @username when there is no name. */
  name: string;
  /** The profile picture Telegram serves from t.me, when the client sent one. */
  photoUrl?: string;
};

/**
 * Reads the customer from a Mini App's initData, for the "Sign in as" button.
 *
 * This is display only. The API checks the signature over the whole string
 * when the button is pressed; nothing here is trusted to sign anybody in.
 */
export function miniAppUser(initData: string): MiniAppUser | null {
  try {
    const raw = new URLSearchParams(initData).get("user");
    if (!raw) {
      return null;
    }
    const user = JSON.parse(raw) as {
      first_name?: string;
      last_name?: string;
      username?: string;
      photo_url?: string;
    };
    const name =
      [user.first_name, user.last_name].filter(Boolean).join(" ").trim() ||
      (user.username ? `@${user.username}` : "");
    if (!name) {
      return null;
    }
    const photoUrl =
      typeof user.photo_url === "string" && user.photo_url.startsWith("https://t.me/")
        ? user.photo_url
        : undefined;
    return { name, photoUrl };
  } catch {
    return null;
  }
}
