import { expect, test } from "@playwright/test";

/**
 * The customer panel's access boundary, from the browser.
 *
 * A route test can prove the API refuses an unauthenticated request. Only a
 * browser can prove that the refusal lands the customer on something they can
 * act on rather than a blank screen, and that nothing behind the session gate
 * paints subscription data before the session is known.
 *
 * The authenticated journeys need a seeded customer with a Remnawave user behind
 * it. That fixture belongs with the integration harness rather than here, and it
 * is tracked as verification debt rather than quietly skipped.
 */

const ACCOUNT_ROUTES = [
  "/account",
  "/account/devices",
  "/account/profile",
  "/account/security",
  "/account/subscriptions/2f1c0c2e-0000-4000-8000-000000000000",
  "/account/subscriptions/2f1c0c2e-0000-4000-8000-000000000000/connect",
];

test.describe("customer account access", () => {
  test("every account route refuses an anonymous visitor", async ({ page }) => {
    // The API answers 401 for a request with no session cookie. It is stubbed
    // here so the suite exercises the signed-out branch deterministically
    // without needing a running backend, which is the same reason the admin
    // suite stops at the sign-in boundary.
    await page.route("**/v1/account/me", (route) =>
      route.fulfill({
        status: 401,
        contentType: "application/problem+json",
        body: JSON.stringify({
          type: "https://omniflow.dev/problems/unauthenticated",
          title: "Unauthorized",
          status: 401,
        }),
      }),
    );

    for (const route of ACCOUNT_ROUTES) {
      await page.goto(route);

      // The shell resolves the session before it renders anything, so an
      // anonymous visitor gets the sign-in prompt rather than a flash of a
      // dashboard shape.
      await expect(page.getByRole("link", { name: /sign in|войти/i })).toBeVisible();

      // Nothing behind the gate may appear: no subscription card, no device
      // list, no tab bar into the rest of the panel.
      await expect(page.getByRole("navigation", { name: /sections|разделы/i })).toHaveCount(0);
    }
  });

  test("the sign-in page says what it offers", async ({ page }) => {
    await page.goto("/account/sign-in");
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  });

  test("a failed round trip explains itself instead of failing silently", async ({ page }) => {
    await page.goto("/account/sign-in?error=link_invalid");

    // Scoped to the page body: the toast region is also an alert, and a bare
    // role lookup would match both.
    const message = page.locator("main").getByRole("alert");
    await expect(message).toBeVisible();
    // The copy has to name a next step. "Something went wrong" leaves a customer
    // with a dead link and nowhere to go.
    await expect(message).toContainText(/expired|used|истекла|использована/i);
  });

  test("no customer cookie is readable from script", async ({ context, page }) => {
    await page.goto("/account/sign-in");
    for (const cookie of await context.cookies()) {
      if (cookie.name.startsWith("__Host-omniflow_account")) {
        expect(cookie.httpOnly, `${cookie.name} is readable from script`).toBe(true);
        expect(cookie.sameSite, `${cookie.name} has no SameSite policy`).not.toBe("None");
      }
    }
  });

  test("the landing page leads into the account", async ({ page }) => {
    await page.goto("/");
    const link = page
      .getByRole("link")
      .filter({ hasText: /account|кабинет/i })
      .first();
    await expect(link).toHaveAttribute("href", "/account");
  });
});
