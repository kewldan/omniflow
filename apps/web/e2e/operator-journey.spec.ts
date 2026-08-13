import { expect, type Locator, test } from "@playwright/test";

/**
 * Fills a field and makes sure the value survived.
 *
 * A form filled before React has hydrated loses what was typed: the controlled
 * inputs mount with their default values and overwrite it. Chromium usually
 * misses the window; WebKit hydrates on a different schedule and hits it
 * reliably. Retrying the fill together with its assertion closes the race
 * without a sleep. The same helper guards `admin-access.spec.ts`, for the same
 * reason.
 */
async function typeInto(field: Locator, value: string): Promise<void> {
  await expect(async () => {
    await field.fill(value);
    await expect(field).toHaveValue(value, { timeout: 1000 });
  }).toPass({ timeout: 10_000 });
}

/**
 * The operator journey behind the session gate.
 *
 * Every other spec in this suite stops at the sign-in screen, because the
 * journeys past it need a seeded operator. CI seeds one — it applies the
 * migrations, starts the API, and redeems the one-time setup token — and passes
 * the credentials in through the two variables below. A run without them skips
 * rather than pretends, and says so.
 *
 * What this covers that a route test cannot: signing in is a browser-side
 * property. The API can answer 200 and set a cookie the browser then discards,
 * or the panel can render and be unable to reach the API at all, and in both
 * cases every server-side test still passes while nobody can use the product.
 * Both of those shipped.
 *
 * It is one test rather than several deliberately. Sign-in is rate limited to
 * ten attempts a minute for an account, which is a control worth having and not
 * worth weakening for a test suite; a `beforeEach` that signed in again for
 * every assertion spent that budget across three browser projects and failed on
 * the limiter rather than on the product.
 */

const EMAIL = process.env.PANEL_EMAIL ?? "";
const PASSWORD = process.env.PANEL_PASSWORD ?? "";

test.describe("operator journey", () => {
  test.skip(
    !EMAIL || !PASSWORD,
    "no seeded operator: set PANEL_EMAIL and PANEL_PASSWORD against a running stack",
  );

  test("an operator signs in, stays signed in, and is refused once signed out", async ({
    page,
    context,
  }) => {
    await page.goto("/admin");
    await expect(page, "an anonymous visitor is sent to sign in").toHaveURL(/\/admin\/login/);

    await typeInto(page.locator('input[type="email"]'), EMAIL);
    await typeInto(page.locator('input[type="password"]'), PASSWORD);
    await page.click('button[type="submit"]');
    await expect(page, "sign-in lands inside the panel").not.toHaveURL(/\/admin\/login/, {
      timeout: 20_000,
    });

    // The cookie has to be one the browser actually kept. A `__Host-` name
    // without `Secure` is discarded on arrival, which is invisible from the
    // server's side: it sees itself set a cookie and never sees it again.
    const session = (await context.cookies()).find((cookie) =>
      /^(__Host-)?omniflow_admin$/.test(cookie.name),
    );
    expect(session, "the browser kept the session cookie").toBeTruthy();
    expect(session?.httpOnly, "the session cookie is unreadable from script").toBe(true);
    expect(
      session?.name.startsWith("__Host-"),
      "the __Host- prefix is present exactly when the cookie is Secure",
    ).toBe(session?.secure);

    // A fresh navigation exercises the middleware gate and the server-side
    // session read, neither of which is involved in the sign-in response.
    await page.goto("/admin");
    await expect(page, "the session survives a fresh page load").not.toHaveURL(/\/admin\/login/);

    // The panel resolved its data rather than rendering an error state. Not a
    // specific figure — those depend on the installation — but evidence that
    // the browser's calls to the API completed.
    await expect(page.locator("main")).toBeVisible();
    await expect(page.locator("main")).not.toContainText(/failed to load|не удалось загрузить/i);

    await context.clearCookies();
    await page.goto("/admin");
    await expect(page, "losing the session returns the operator to sign-in").toHaveURL(
      /\/admin\/login/,
    );
  });

  // The localisation gate in `gates.spec.ts` runs against the pages reachable
  // without a session, which is most of the copy nobody wrote but none of the
  // panel. Two missing translations shipped behind this gate: the sidebar
  // rendered `admin.navigation.items.offers`, and every dashboard metric
  // rendered its own definition key, because the catalogue stored those keys
  // flat while next-intl resolves a dot as a nesting separator.
  test("no panel page renders a raw message key", async ({ page }) => {
    await page.goto("/admin/login");
    await typeInto(page.locator('input[type="email"]'), EMAIL);
    await typeInto(page.locator('input[type="password"]'), PASSWORD);
    await page.click('button[type="submit"]');
    await expect(page).not.toHaveURL(/\/admin\/login/, { timeout: 20_000 });

    const PANEL_PAGES = [
      "/admin",
      "/admin/customers",
      "/admin/finance",
      "/admin/catalog",
      "/admin/support",
      "/admin/marketing",
      "/admin/system",
      "/admin/settings",
      "/admin/audit",
      "/admin/operators",
      "/admin/risk",
      "/admin/security",
      "/admin/shop",
      "/admin/gifts",
      "/admin/offers",
    ];

    for (const path of PANEL_PAGES) {
      await page.goto(path);
      await expect(page.locator("main")).toBeVisible();
      const body = (await page.locator("body").innerText()) ?? "";
      // next-intl renders the key itself when a message is missing, which looks
      // like copy nobody wrote and reads as `admin.navigation.items.offers`.
      expect(body, `${path} rendered an untranslated message key`).not.toMatch(
        /\badmin\.[a-z][A-Za-z]*\.[a-z][A-Za-z_.]*\b/,
      );
    }
  });
});
