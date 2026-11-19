import { type Browser, type BrowserContext, expect, type Page, test } from "@playwright/test";

import { MISSING_MESSAGE_MARKER } from "../i18n/missing-message";
import { contextFor, type StorageState } from "./contexts";

/**
 * The operator's everyday screens, at every viewport the suite runs.
 *
 * `operator-journey.spec.ts` proves an operator can sign in and that no page
 * renders a raw key. This covers the screens a business is run from — the
 * dashboard, the customer list, orders, support, settings — and the defects
 * found walking them: a tab title that repeated the brand, a breadcrumb that
 * was invalid markup on every page, tab rows wider than a phone, and a panel
 * that could not be switched out of English.
 *
 * Sign-in is rate limited per address, so the session is made once per worker
 * through the API and reused.
 */

const EMAIL = process.env.PANEL_EMAIL ?? "";
const PASSWORD = process.env.PANEL_PASSWORD ?? "";

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

/** Collects the React markup and hydration errors a page logs. */
function watchMarkupErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on("console", (message) => {
    if (
      message.type() === "error" &&
      /hydrat|cannot be a descendant|cannot contain a nested/i.test(message.text())
    ) {
      errors.push(message.text().slice(0, 200));
    }
  });
  return errors;
}

async function saveLocale(context: BrowserContext, locale: "en" | "ru") {
  const session = await (await context.request.get("/v1/panel/auth/session")).json();
  const response = await context.request.patch("/v1/panel/auth/profile", {
    data: {
      displayName: session.account.displayName,
      locale,
      timezone: session.account.timezone,
    },
    headers: { "X-CSRF-Token": session.csrfToken },
  });
  expect(response.ok(), await response.text()).toBe(true);
}

test.describe("operator panel", () => {
  test.skip(
    !EMAIL || !PASSWORD,
    "no seeded operator: set PANEL_EMAIL and PANEL_PASSWORD against a running stack",
  );

  let storageState: StorageState;

  test.beforeAll(async ({ browser }, workerInfo) => {
    const context = await browser.newContext({ baseURL: workerInfo.project.use.baseURL });
    const response = await context.request.post("/v1/panel/auth/login", {
      data: { email: EMAIL, password: PASSWORD },
    });
    expect(response.ok(), await response.text()).toBe(true);
    await saveLocale(context, "en");
    storageState = await context.storageState();
    await context.close();
  });

  async function signedIn(browser: Browser) {
    const context = await contextFor(browser, storageState);
    return { context, page: await context.newPage() };
  }

  test("the dashboard greets the operator, titles the tab once, and fits", async ({ browser }) => {
    const { context, page } = await signedIn(browser);
    const markup = watchMarkupErrors(page);
    await page.goto("/admin");
    await expect(page.getByRole("heading", { level: 1 })).toContainText(/^Hello, /);
    // A layout's default title was run through the root template, which put
    // the installation's name in the tab twice.
    const title = await page.title();
    const parts = title.split(" · ");
    expect(new Set(parts).size, `"${title}" repeats a part`).toBe(parts.length);
    await noHorizontalScroll(page, "dashboard");
    await noMissingMessages(page, "dashboard");
    expect(markup, markup.join("\n")).toEqual([]);
    await context.close();
  });

  test("the customer list searches and says when nothing matches", async ({ browser }) => {
    const { context, page } = await signedIn(browser);
    const markup = watchMarkupErrors(page);
    await page.goto("/admin/customers");
    await expect(page.getByRole("heading", { level: 1, name: "Customers" })).toBeVisible();
    // The breadcrumb below the dashboard was an <li> inside an <li>.
    await expect(page.getByRole("navigation", { name: /breadcrumb/i })).toContainText("Customers");

    const search = page.getByLabel("Identifier");
    await search.fill("no-such-customer-anywhere");
    await search.press("Tab");
    await expect(page).toHaveURL(/q=no-such-customer-anywhere/);
    await expect(page.getByText("No matches")).toBeVisible();
    await page.getByRole("button", { name: /clear/i }).first().click();
    await expect(page).not.toHaveURL(/q=/);
    await noHorizontalScroll(page, "customers");
    expect(markup, markup.join("\n")).toEqual([]);
    await context.close();
  });

  test("orders open with their tabs inside the screen", async ({ browser }) => {
    const { context, page } = await signedIn(browser);
    await page.goto("/admin/finance");
    await expect(page.getByRole("heading", { level: 1, name: "Finance" })).toBeVisible();
    // On a phone the three tabs used to widen the whole page.
    const tabs = page.getByRole("tablist").first();
    await expect(tabs.getByRole("tab", { name: "Orders" })).toBeVisible();
    await noHorizontalScroll(page, "finance");
    const last = tabs.getByRole("tab").last();
    await last.scrollIntoViewIfNeeded();
    await last.click();
    await expect(last).toHaveAttribute("aria-selected", "true");
    await noMissingMessages(page, "finance");
    await context.close();
  });

  test("the support desk opens without an error and fits", async ({ browser }) => {
    const { context, page } = await signedIn(browser);
    await page.goto("/admin/support");
    await expect(page.getByRole("heading", { level: 1, name: "Support desk" })).toBeVisible();
    await expect(page.getByText("Could not load the tickets")).toHaveCount(0);
    // Ticket rows and queue tiles are buttons, so a keyboard reaches them.
    const rows = page.locator("main button[aria-pressed], main button[aria-current]");
    if ((await rows.count()) > 0) {
      await rows.first().focus();
      await expect(rows.first()).toBeFocused();
    }
    await noHorizontalScroll(page, "support");
    await noMissingMessages(page, "support");
    await context.close();
  });

  test("every settings area has a heading and fits", async ({ browser }) => {
    const { context, page } = await signedIn(browser);
    await page.goto("/admin/settings");
    await expect(page.getByRole("heading", { level: 1, name: "Settings" })).toBeVisible();
    const areas = await page
      .locator('main a[href^="/admin/settings/"]')
      .evaluateAll((links) => links.map((link) => link.getAttribute("href") ?? ""));
    expect(areas.length).toBeGreaterThan(0);
    test.setTimeout(30_000 + areas.length * 8_000);
    for (const href of areas) {
      await page.goto(href);
      await expect(page.getByRole("heading", { level: 1 }), `${href} has no heading`).toBeVisible();
      await noHorizontalScroll(page, href);
      await noMissingMessages(page, href);
    }
    await context.close();
  });

  test("the operator can switch the panel to Russian and back", async ({ browser }) => {
    const { context, page } = await signedIn(browser);
    try {
      await page.goto("/admin");
      await expect(page.locator("html")).toHaveAttribute("lang", "en");
      await page.getByRole("button", { name: "Account menu" }).click();
      await page.getByRole("menuitem", { name: "Русский" }).click();
      await expect(page.locator("html")).toHaveAttribute("lang", "ru");
      await expect(page.getByRole("heading", { level: 1 })).toContainText(/^Здравствуйте, /);
      await noMissingMessages(page, "dashboard (ru)");

      // Saved on the account, so a fresh context — another browser — follows.
      const other = await contextFor(browser, storageState);
      const second = await other.newPage();
      await second.goto("/admin/customers");
      await expect(second.locator("html")).toHaveAttribute("lang", "ru");
      await expect(second.getByRole("heading", { level: 1 })).toHaveText("Клиенты");
      await other.close();
    } finally {
      await saveLocale(context, "en");
      await context.close();
    }
  });
});
