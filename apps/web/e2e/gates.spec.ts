import { expect, test } from "@playwright/test";

import { MISSING_MESSAGE_MARKER } from "../i18n/missing-message";
import { seriousViolations } from "./axe";

/**
 * The accessibility, layout, and localisation gates.
 *
 * They live in the browser suite because all three are properties of a rendered
 * page rather than of a component: a contrast failure depends on the theme that
 * actually applied, a horizontal scrollbar depends on the viewport, and a
 * missing translation only shows up when the catalogue is loaded for real.
 *
 * The pages checked here are the ones reachable without a session. The
 * signed-in pages get the same accessibility check from the customer and
 * operator journeys, which sign in and sweep every panel page (see `axe.ts`).
 */

const PUBLIC_PAGES = ["/", "/admin/login", "/account/sign-in"];

test.describe("accessibility", () => {
  for (const path of PUBLIC_PAGES) {
    test(`${path} has no serious or critical violations`, async ({ page }) => {
      await page.goto(path);
      const blocking = await seriousViolations(page, path);
      expect(blocking, blocking.join("\n")).toEqual([]);
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
      // A missing message renders as the marker configured in i18n/request.ts,
      // which cannot collide with content and covers every namespace rather
      // than the two this pattern used to name.
      expect(body, `${path} rendered an untranslated message key`).not.toContain(
        MISSING_MESSAGE_MARKER,
      );
    }
  });

  test("the document declares its language", async ({ page }) => {
    await page.goto("/admin/login");
    // Without it a screen reader guesses the pronunciation, and it guesses in
    // whatever language it was configured for.
    await expect(page.locator("html")).toHaveAttribute("lang", /^(en|ru)/);
  });
});

test.describe("content security policy", () => {
  for (const path of PUBLIC_PAGES) {
    test(`${path} carries a nonce policy and breaks nothing under it`, async ({ page }) => {
      const violations: string[] = [];
      page.on("console", (message) => {
        if (
          /Content.Security.Policy|Refused to (execute|load|apply|frame|connect)/i.test(
            message.text(),
          )
        ) {
          violations.push(message.text());
        }
      });

      const response = await page.goto(path);
      const policy = response?.headers()["content-security-policy"] ?? "";
      expect(policy).toMatch(/script-src [^;]*'nonce-[A-Za-z0-9+/=]+'/);
      expect(policy).toContain("object-src 'none'");
      // The page has to hydrate under it: the theme bootstrap and Next.js's
      // own inline scripts all need the nonce, and a missing one is silent
      // apart from the console.
      await page.waitForLoadState("networkidle");
      expect(violations, violations.join("\n")).toEqual([]);
    });
  }

  test("the operator panel admits no third-party origin and cannot be framed", async ({ page }) => {
    const response = await page.goto("/admin/login");
    const policy = response?.headers()["content-security-policy"] ?? "";
    expect(policy).toContain("frame-ancestors 'none'");
    expect(policy).not.toMatch(/yandex|google|telegram/);
  });
});
