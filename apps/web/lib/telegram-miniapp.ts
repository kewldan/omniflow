/**
 * Recognising the customer panel inside Telegram, without Telegram's own
 * script (a third-party script the CSP keeps out). Talking to Telegram —
 * expanding, full screen, haptics, the back button — goes through tma.js in
 * lib/tma.ts; this reads the launch fragment and Telegram's user.
 */

/** sessionStorage key remembering that this tab was opened as a Mini App. */
const MINI_APP_FLAG = "omniflow.miniapp";

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
