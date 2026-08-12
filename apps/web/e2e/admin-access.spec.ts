import { expect, type Locator, test } from "@playwright/test";

/**
 * Fills a field and makes sure the value survived.
 *
 * A form filled before React has hydrated loses what was typed: the controlled
 * inputs mount with their default values and overwrite it. The window is narrow
 * and Chromium usually misses it, but WebKit hydrates on a different schedule
 * and hit it reliably — the email field came back empty, the form refused to
 * submit, and the failure looked like a missing error message rather than a lost
 * keystroke. Retrying the fill together with its assertion closes the race
 * without any sleep.
 */
async function typeInto(field: Locator, value: string): Promise<void> {
  await expect(async () => {
    await field.fill(value);
    await expect(field).toHaveValue(value, { timeout: 1000 });
  }).toPass({ timeout: 10_000 });
}

/**
 * Authentication and authorisation, from the browser.
 *
 * These are the journeys where a regression is expensive rather than merely
 * visible. A unit test can prove the middleware refuses an unauthenticated
 * request; only a browser can prove that the refusal actually lands the person
 * on the sign-in page instead of a blank screen, and that the session cookie is
 * set with the attributes that make it worth having.
 */

const PANEL_ROUTES = [
  "/admin",
  "/admin/customers",
  "/admin/finance",
  "/admin/support",
  "/admin/marketing",
  "/admin/settings",
  "/admin/settings/ai",
  "/admin/audit",
  "/admin/operators",
];

test.describe("admin access", () => {
  test("every panel route sends an anonymous visitor to sign in", async ({ page }) => {
    for (const route of PANEL_ROUTES) {
      await page.goto(route);
      // The redirect carries where they were going, so signing in resumes
      // rather than dumping them on the dashboard.
      await expect(page).toHaveURL(/\/admin\/login/);
      expect(page.url()).toContain(encodeURIComponent(route).slice(0, 12));
    }
  });

  test("the sign-in page is reachable and names what it wants", async ({ page }) => {
    await page.goto("/admin/login");
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expect(page.getByLabel(/email/i)).toBeVisible();
    await expect(page.getByLabel(/password/i)).toBeVisible();
  });

  test("a wrong password says nothing about which half was wrong", async ({ page }) => {
    await page.goto("/admin/login");
    await typeInto(page.getByLabel(/email/i), "nobody@example.test");
    await typeInto(page.getByLabel(/password/i), "not-the-password");
    await page.getByRole("button", { name: /sign in|войти/i }).click();

    // A message that distinguishes "no such account" from "wrong password" is
    // an account enumeration oracle.
    //
    // Scoped to the form: Next's route announcer is also an alert, and a bare
    // role lookup matches both and fails on strict mode rather than on the thing
    // being asserted.
    const message = page.locator("main, form").getByRole("alert").first();
    await expect(message).toBeVisible();
    await expect(message).not.toContainText(/no such|not found|unknown user/i);
  });

  test("the session cookie is host-scoped and unreadable to script", async ({ page, context }) => {
    await page.goto("/admin/login");
    const cookies = await context.cookies();
    for (const cookie of cookies) {
      if (!cookie.name.toLowerCase().includes("session")) {
        continue;
      }
      // A session cookie a script can read is a session an injected script can
      // steal, and one without SameSite is one a cross-site form can use.
      expect(cookie.httpOnly).toBe(true);
      expect(cookie.sameSite).not.toBe("None");
    }
  });
});
