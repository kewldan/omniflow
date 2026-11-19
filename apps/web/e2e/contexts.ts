import type { Browser, BrowserContext, BrowserContextOptions } from "@playwright/test";
import { test } from "@playwright/test";

export type StorageState = Awaited<ReturnType<BrowserContext["storageState"]>>;

/**
 * A new context with the running project's device and an existing session.
 *
 * The panel specs sign in once per worker — sign-in is rate limited, and a
 * fresh one per test fails on the limiter rather than on the product — and open
 * each test in a context of its own made from that session. The project's
 * `use` block carries runner-only keys as well as context options, so only the
 * context options are passed on.
 */
export async function contextFor(
  browser: Browser,
  storageState: StorageState,
  overrides: BrowserContextOptions = {},
): Promise<BrowserContext> {
  const use = test.info().project.use;
  return browser.newContext({
    baseURL: use.baseURL,
    deviceScaleFactor: use.deviceScaleFactor,
    hasTouch: use.hasTouch,
    isMobile: use.isMobile,
    locale: use.locale,
    userAgent: use.userAgent,
    viewport: use.viewport,
    ...overrides,
    storageState,
  });
}
