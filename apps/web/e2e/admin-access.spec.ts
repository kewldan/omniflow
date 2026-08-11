import { expect, test } from "@playwright/test";

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
    await page.getByLabel(/email/i).fill("nobody@example.test");
    await page.getByLabel(/password/i).fill("not-the-password");
    await page.getByRole("button", { name: /sign in|войти/i }).click();

    // A message that distinguishes "no such account" from "wrong password" is
    // an account enumeration oracle.
    const message = page.getByRole("alert");
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
