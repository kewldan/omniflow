import { createHash, createHmac } from "node:crypto";

import { expect, test } from "@playwright/test";

import { MISSING_MESSAGE_MARKER } from "../i18n/missing-message";

/**
 * The customer journey behind the session gate.
 *
 * The operator half of this was covered first and immediately found three
 * defects, two of which meant nobody could use the panel from a browser at all.
 * This is the same shape of coverage for the customer surface, and it exists for
 * the same reason: the API can answer 200 and set a cookie the browser then
 * discards, or the panel can render and be unable to reach the API, and every
 * server-side test still passes while nobody can use the product.
 *
 * Signing in is done the way a customer's browser does it — a signed Telegram
 * login-widget payload posted to the real API through the web server's proxy —
 * rather than by writing a session row. A fixture that inserted a session would
 * skip the two things most likely to be broken: the signature check and the
 * cookie the browser has to keep.
 *
 * The widget is signed here with the same bot token the API is configured with.
 * CI passes it in; a run without it skips rather than pretends, and says so.
 *
 * What this does not cover is anything behind a Remnawave entitlement. The
 * customer it creates has signed in and bought nothing, which is a real state —
 * it is every customer's first minute — and the screens that need a subscription
 * render their empty state rather than their populated one.
 */

const BOT_TOKEN = process.env.CUSTOMER_BOT_TOKEN ?? "";
const TELEGRAM_ID = process.env.CUSTOMER_TELEGRAM_ID ?? "770001";

/**
 * Builds a login-widget payload Telegram would have produced.
 *
 * Every field except the hash participates, sorted, newline-joined, and signed
 * with HMAC-SHA256 under the SHA-256 of the bot token. That is Telegram's scheme
 * and `internal/customerauth` verifies exactly it, so a mistake here fails the
 * test rather than weakening the check.
 */
function signedWidget(botToken: string): Record<string, string> {
  const fields: Record<string, string> = {
    auth_date: String(Math.floor(Date.now() / 1000)),
    first_name: "Playwright",
    id: TELEGRAM_ID,
  };
  const payload = Object.keys(fields)
    .sort()
    .map((key) => `${key}=${fields[key]}`)
    .join("\n");
  const secret = createHash("sha256").update(botToken).digest();
  return { ...fields, hash: createHmac("sha256", secret).update(payload).digest("hex") };
}

test.describe("customer journey", () => {
  test.skip(
    !BOT_TOKEN,
    "no bot token: set CUSTOMER_BOT_TOKEN to the token the API is configured with",
  );

  test("a customer signs in, stays signed in, and is refused once signed out", async ({
    page,
    context,
  }) => {
    await page.goto("/account");
    await expect(page, "an anonymous visitor is sent to sign in").toHaveURL(/\/account\/sign-in/);

    // Through the same origin the browser uses, so the web server's /v1 proxy is
    // exercised rather than bypassed, and the cookie lands in the context the
    // page will navigate with.
    const response = await context.request.post("/v1/account/auth/telegram", {
      data: signedWidget(BOT_TOKEN),
    });
    expect(response.status(), await response.text()).toBe(200);

    const session = (await context.cookies()).find((cookie) =>
      /^(__Host-)?omniflow_account$/.test(cookie.name),
    );
    expect(session, "the browser kept the session cookie").toBeTruthy();
    expect(session?.httpOnly, "the session cookie is unreadable from script").toBe(true);
    expect(
      session?.name.startsWith("__Host-"),
      "the __Host- prefix is present exactly when the cookie is Secure",
    ).toBe(session?.secure);
    // The customer and operator cookies must not be the same name, or one panel
    // signs the other out whenever both are open.
    expect(session?.name).not.toMatch(/omniflow_admin/);

    await page.goto("/account");
    await expect(page, "the session survives a fresh page load").not.toHaveURL(
      /\/account\/sign-in/,
    );
    await expect(page.locator("main")).toBeVisible();
    await expect(page.locator("main")).not.toContainText(/failed to load|не удалось загрузить/i);

    await context.clearCookies();
    await page.goto("/account");
    await expect(page, "losing the session returns the customer to sign-in").toHaveURL(
      /\/account\/sign-in/,
    );
  });

  test("no customer page renders a raw message key", async ({ page, context }) => {
    const response = await context.request.post("/v1/account/auth/telegram", {
      data: signedWidget(BOT_TOKEN),
    });
    expect(response.status(), await response.text()).toBe(200);

    // Every customer page reachable without an identifier in the path. The ones
    // that need a subscription, an order, or a ticket are covered by their empty
    // state on the page that lists them.
    const CUSTOMER_PAGES = [
      "/account",
      "/account/store",
      "/account/shop",
      "/account/orders",
      "/account/wallet",
      "/account/devices",
      "/account/support",
      "/account/support/new",
      "/account/news",
      "/account/referrals",
      "/account/profile",
      "/account/preferences",
      "/account/security",
      "/account/privacy",
    ];

    for (const path of CUSTOMER_PAGES) {
      await page.goto(path);
      await expect(page.locator("main"), `${path} rendered nothing`).toBeVisible();
      const body = (await page.locator("body").innerText()) ?? "";
      expect(body, `${path} rendered an untranslated message key`).not.toContain(
        MISSING_MESSAGE_MARKER,
      );
    }
  });
});
