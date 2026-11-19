/**
 * A User-Agent string reduced to what a person recognises: "Chrome · macOS".
 *
 * The sessions list used to print the raw header, which is two lines of
 * `Mozilla/5.0 (Macintosh; …) AppleWebKit/537.36 (KHTML, like Gecko) …` that
 * every browser starts the same way, so an operator could not tell their
 * laptop from their phone. This is deliberately coarse — a family name, no
 * versions — because it labels a row rather than fingerprints a device, and
 * the raw string stays available on hover.
 *
 * Order matters: Edge, Opera, Yandex and Samsung all also claim "Chrome", and
 * Chrome also claims "Safari", so the more specific tokens are tested first.
 */

const BROWSERS: [RegExp, string][] = [
  [/\bEdg(?:e|A|iOS)?\//, "Edge"],
  [/\bOPR\/|\bOpera\b/, "Opera"],
  [/\bYaBrowser\//, "Yandex Browser"],
  [/\bSamsungBrowser\//, "Samsung Internet"],
  [/\bFirefox\/|\bFxiOS\//, "Firefox"],
  [/\bHeadlessChrome\//, "Headless Chrome"],
  [/\bChrome\/|\bCriOS\//, "Chrome"],
  [/\bVersion\/[\d.]+.*\bSafari\//, "Safari"],
  [/\bTelegram/i, "Telegram"],
];

const SYSTEMS: [RegExp, string][] = [
  [/\biPhone\b/, "iPhone"],
  [/\biPad\b/, "iPad"],
  [/\bAndroid\b/, "Android"],
  [/\bCrOS\b/, "ChromeOS"],
  [/\bMac OS X\b|\bMacintosh\b/, "macOS"],
  [/\bWindows\b/, "Windows"],
  [/\bLinux\b/, "Linux"],
];

export type DescribedAgent = {
  browser?: string;
  system?: string;
  /** Whether the system is a phone or tablet, for choosing an icon. */
  mobile: boolean;
};

export function describeUserAgent(userAgent: string | null | undefined): DescribedAgent {
  const value = userAgent ?? "";
  const browser = BROWSERS.find(([pattern]) => pattern.test(value))?.[1];
  const system = SYSTEMS.find(([pattern]) => pattern.test(value))?.[1];
  const mobile = /\b(?:iPhone|iPad|Android|Mobile)\b/.test(value);
  return { browser, mobile, system };
}

/** "Chrome · macOS", or whichever half is known, or null for nothing usable. */
export function userAgentLabel(userAgent: string | null | undefined): string | null {
  const { browser, system } = describeUserAgent(userAgent);
  const parts = [browser, system].filter(Boolean);
  return parts.length > 0 ? parts.join(" · ") : null;
}
