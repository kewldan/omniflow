import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";

/**
 * The accessibility, layout, and localisation gates.
 *
 * They live in the browser suite because all three are properties of a rendered
 * page rather than of a component: a contrast failure depends on the theme that
 * actually applied, a horizontal scrollbar depends on the viewport, and a
 * missing translation only shows up when the catalogue is loaded for real.
 *
 * The pages checked here are the ones reachable without a session. That is a
 * genuine limitation and is stated rather than hidden: signing in needs a
 * seeded operator, and the fixture for that belongs with the integration
 * harness. What these gates do catch is a regression in the shell, the theme,
 * and the forms — which is where most accessibility regressions actually
 * happen.
 */

const PUBLIC_PAGES = ["/", "/admin/login", "/account/sign-in"];

test.describe("accessibility", () => {
  for (const path of PUBLIC_PAGES) {
    test(`${path} has no serious or critical violations`, async ({ page }) => {
      await page.goto(path);
      const results = await new AxeBuilder({ page })
        .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
        .analyze();

      // Serious and critical only. Gating on every advisory rule produces a
      // list nobody reads and a gate somebody disables.
      const blocking = results.violations.filter(
        (violation) => violation.impact === "serious" || violation.impact === "critical",
      );
      expect(
        blocking,
        blocking.map((violation) => `${violation.id}: ${violation.help}`).join("\n"),
      ).toEqual([]);
    });
  }

  test("the sign-in form is operable by keyboard alone", async ({ page }) => {
    await page.goto("/admin/login");
    await page.keyboard.press("Tab");

    // Something focusable must be reachable, and focus must be visible: a focus
    // ring removed for looks is a page a keyboard user cannot navigate.
    const focused = page.locator(":focus");
    await expect(focused).toBeVisible();
  });
});

test.describe("responsive layout", () => {
  for (const path of PUBLIC_PAGES) {
    test(`${path} never scrolls horizontally`, async ({ page }) => {
      await page.goto(path);
      // A page wider than its viewport is the single most common mobile layout
      // bug and the easiest to assert.
      const overflowing = await page.evaluate(
        () => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
      );
      expect(overflowing).toBe(false);
    });
  }
});

test.describe("localisation", () => {
  test("no page renders a raw message key", async ({ page }) => {
    for (const path of PUBLIC_PAGES) {
      await page.goto(path);
      const body = (await page.locator("body").innerText()) ?? "";
      // next-intl renders the key itself when a message is missing, which looks
      // like copy nobody wrote and reads as `admin.login.title`.
      expect(body).not.toMatch(/\b(admin|home)\.[a-z][A-Za-z]*\.[a-z][A-Za-z]*\b/);
    }
  });

  test("the document declares its language", async ({ page }) => {
    await page.goto("/admin/login");
    // Without it a screen reader guesses the pronunciation, and it guesses in
    // whatever language it was configured for.
    await expect(page.locator("html")).toHaveAttribute("lang", /^(en|ru)/);
  });
});
