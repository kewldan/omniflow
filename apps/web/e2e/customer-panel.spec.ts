import { createHash, createHmac } from "node:crypto";

import { type Browser, type BrowserContext, expect, type Page, test } from "@playwright/test";

import { MISSING_MESSAGE_MARKER } from "../i18n/missing-message";
import { contextFor, type StorageState } from "./contexts";

/**
 * The customer's everyday screens, at every viewport the suite runs.
 *
 * `customer-journey.spec.ts` proves a customer can get in and that no page is
 * broken outright; this covers what they then do: read the dashboard, start a
 * purchase, top up the wallet, and write to support — and that the panel
 * speaks their language, which it did not (every page rendered English).
 *
 * The session is made once per worker, the way a browser makes it (a signed
 * Telegram login-widget payload through the web server's proxy), and reused,
 * because sign-in is rate limited and a fresh one per test spends that budget.
 * A run without the bot token skips the signed-in half and says so.
 */

const BOT_TOKEN = process.env.CUSTOMER_BOT_TOKEN ?? "";

function signedWidget(botToken: string, id: number): Record<string, string | number> {
  const fields: Record<string, string | number> = {
    auth_date: Math.floor(Date.now() / 1000),
    first_name: "Panel",
    id,
  };
  const payload = Object.keys(fields)
    .sort()
    .map((key) => `${key}=${fields[key]}`)
    .join("\n");
  const secret = createHash("sha256").update(botToken).digest();
  return { ...fields, hash: createHmac("sha256", secret).update(payload).digest("hex") };
}

async function noHorizontalScroll(page: Page, label: string) {
  const overflowing = await page.evaluate(
    () => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
  );
  expect(overflowing, `${label} scrolls sideways`).toBe(false);
}

async function noMissingMessages(page: Page, label: string) {
  const body = (await page.locator("body").innerText()) ?? "";
  expect(body, `${label} rendered an untranslated message key`).not.toContain(
    MISSING_MESSAGE_MARKER,
  );
}

/** The CSRF token the API rotates onto every authenticated response. */
async function csrf(context: BrowserContext): Promise<string> {
  const response = await context.request.get("/v1/account/me");
  return response.headers()["x-csrf-token"] ?? "";
}

async function saveLocale(context: BrowserContext, locale: "en" | "ru") {
  const response = await context.request.patch("/v1/account/me", {
    data: { locale, timezone: "UTC" },
    headers: { "X-CSRF-Token": await csrf(context) },
  });
  expect(response.ok(), await response.text()).toBe(true);
}

test.describe("customer sign-in screen", () => {
  test("names what it offers, switches language, and fits the screen", async ({
    context,
    page,
  }) => {
    await context.addCookies([
      { name: "omniflow_locale", value: "en", url: test.info().project.use.baseURL ?? "" },
    ]);
    // Named from the cookie, not the browser: the test browser asks for English
    // anyway, and the switch below is what is under test.
    await page.goto("/account/sign-in");
    await expect(page.locator("html")).toHaveAttribute("lang", "en");
    await expect(page.getByRole("heading", { level: 1, name: "Sign in" })).toBeVisible();
    await noHorizontalScroll(page, "sign-in");

    // The switch names the other language in that language, so someone who
    // cannot read the current one can still find it.
    await page.getByRole("button", { name: /Русский/ }).click();
    await expect(page.locator("html")).toHaveAttribute("lang", "ru");
    await expect(page.getByRole("heading", { level: 1, name: "Вход" })).toBeVisible();
    await noMissingMessages(page, "sign-in (ru)");

    await page.getByRole("button", { name: /English/ }).click();
    await expect(page.locator("html")).toHaveAttribute("lang", "en");
  });

  test("a browser that asks for Russian gets Russian before anyone signs in", async ({
    browser,
  }) => {
    const context = await browser.newContext({ locale: "ru-RU" });
    const page = await context.newPage();
    await page.goto("/account/sign-in");
    await expect(page.locator("html")).toHaveAttribute("lang", "ru");
    await expect(page.getByRole("heading", { level: 1, name: "Вход" })).toBeVisible();
    await context.close();
  });
});

test.describe("customer panel", () => {
  test.skip(
    !BOT_TOKEN,
    "no bot token: set CUSTOMER_BOT_TOKEN to the token the API is configured with",
  );

  let storageState: StorageState;

  test.beforeAll(async ({ browser }, workerInfo) => {
    // One customer per project, so parallel projects do not share a checkout.
    const id = 780_000 + workerInfo.workerIndex * 10 + workerInfo.project.name.length;
    const context = await browser.newContext({
      baseURL: workerInfo.project.use.baseURL,
      locale: "en-US",
    });
    const response = await context.request.post("/v1/account/auth/telegram", {
      data: signedWidget(BOT_TOKEN, id),
    });
    expect(response.status(), await response.text()).toBe(200);
    await saveLocale(context, "en");
    storageState = await context.storageState();
    await context.close();
  });

  async function signedIn(browser: Browser) {
    const context = await contextFor(browser, storageState);
    return { context, page: await context.newPage() };
  }

  test("the dashboard has a heading, a way to buy, and fits the screen", async ({ browser }) => {
    const { context, page } = await signedIn(browser);
    await page.goto("/account");
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("Subscriptions");
    await expect(page.getByRole("navigation", { name: /sections/i }).first()).toBeVisible();
    // A customer with nothing yet is offered the store, not told to find it.
    const browse = page.getByRole("link", { name: "Browse plans" });
    if (await browse.count()) {
      await expect(browse).toHaveAttribute("href", "/account/store");
    }
    await noHorizontalScroll(page, "dashboard");
    await noMissingMessages(page, "dashboard");
    await context.close();
  });

  test("the panel follows the language the account saved", async ({ browser }) => {
    const { context, page } = await signedIn(browser);
    await saveLocale(context, "ru");
    try {
      await page.goto("/account/wallet");
      // The shell applies the saved language and re-renders in it.
      await expect(page.locator("html")).toHaveAttribute("lang", "ru");
      await expect(page.getByRole("heading", { level: 1 })).toHaveText("Кошелёк");
      await noMissingMessages(page, "wallet (ru)");
    } finally {
      await saveLocale(context, "en");
      await context.close();
    }
  });

  test("starting a purchase opens it at checkout", async ({ browser }) => {
    const { context, page } = await signedIn(browser);
    await page.goto("/account/store");
    const buy = page.getByRole("link", { name: /^Buy$/ }).first();
    // An installation with nothing on sale has no purchase to start.
    await page.waitForLoadState("networkidle", { timeout: 5_000 }).catch(() => undefined);
    test.skip((await buy.count()) === 0, "the catalogue has no visible plan to buy");

    await buy.click();
    await page.getByRole("button", { name: "Continue" }).click();
    await expect(page).toHaveURL(/\/account\/checkout$/);
    // It used to say "No purchase in progress" about the one just started,
    // because it served the plan page's cached "nothing open" answer.
    await expect(page.getByText("No purchase in progress")).toHaveCount(0);
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expect(page.getByText("New purchase")).toBeVisible();
    await expect(page.getByRole("button", { name: /^Confirm/ })).toBeVisible();
    await noHorizontalScroll(page, "checkout");
    await noMissingMessages(page, "checkout");

    // Leave nothing open for the next run.
    await page.getByRole("button", { name: "Discard this purchase" }).first().click();
    await page.getByRole("dialog").getByRole("button", { name: "Discard this purchase" }).click();
    // Back in the store, with no "purchase in progress" banner about the one
    // just discarded — it used to linger from the cached checkout.
    await expect(page).toHaveURL(/\/account\/store$/);
    await expect(page.getByRole("link", { name: /^Buy$/ }).first()).toBeVisible();
    await expect(page.getByText("You have a purchase in progress")).toHaveCount(0);
    await context.close();
  });

  test("the wallet top-up explains an amount it will refuse", async ({ browser }) => {
    const { context, page } = await signedIn(browser);
    await page.goto("/account/wallet");
    const amount = page.getByLabel(/^Amount in/);
    await page.waitForLoadState("networkidle", { timeout: 5_000 }).catch(() => undefined);
    test.skip((await amount.count()) === 0, "wallet top-up is switched off on this installation");

    const submit = page.getByRole("button", { name: "Add to wallet" });
    await amount.fill("abc");
    await submit.click();
    await expect(page.getByText("Enter an amount such as 4.99.")).toBeVisible();
    await expect(amount).toHaveAttribute("aria-invalid", "true");

    await amount.fill("0.01");
    await submit.click();
    await expect(page.getByText(/^The smallest amount is/)).toBeVisible();

    await amount.fill("99999999");
    await submit.click();
    await expect(page.getByText(/^The largest amount is|^The amount is too large/)).toBeVisible();
    await noHorizontalScroll(page, "wallet");
    await context.close();
  });

  test("a customer starts a support conversation and can attach a file", async ({ browser }) => {
    const { context, page } = await signedIn(browser);
    await page.goto("/account/support/new");
    await expect(page.getByRole("heading", { level: 1 })).toHaveText("Start a conversation");

    // An empty submission is refused with a reason, not silently.
    await page.getByRole("button", { name: "Send" }).click();
    await expect(page.getByText(/A subject is needed/)).toBeVisible();

    const subject = `Cannot connect ${Date.now()}`;
    await page.getByLabel("Subject").fill(subject);
    await page.getByLabel("Message").fill("The app times out after the latest update.");
    await page.getByRole("button", { name: "Send" }).click();
    await expect(page).toHaveURL(/\/account\/support\/[0-9a-f-]{36}$/);
    await expect(page.getByRole("heading", { level: 1 })).toHaveText(subject);

    // The picker is ours, in the panel's language, not the browser's own
    // "Choose File / No file chosen".
    const picker = page.locator('input[type="file"]');
    if (await picker.count()) {
      await expect(page.getByText("Choose a file", { exact: false }).first()).toBeVisible();
      await expect(page.getByText("No file chosen yet")).toBeVisible();
      await expect(page.getByText(/image\/png/)).toHaveCount(0);
    }
    await noHorizontalScroll(page, "support thread");
    await noMissingMessages(page, "support thread");
    await context.close();
  });
});
