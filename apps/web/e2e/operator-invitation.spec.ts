import { expect, type Locator, test } from "@playwright/test";

import { MISSING_MESSAGE_MARKER } from "../i18n/missing-message";

/** See operator-journey.spec.ts: a fill before hydration can be lost. */
async function typeInto(field: Locator, value: string): Promise<void> {
  await expect(async () => {
    await field.fill(value);
    await expect(field).toHaveValue(value, { timeout: 1000 });
  }).toPass({ timeout: 10_000 });
}

const EMAIL = process.env.PANEL_EMAIL ?? "";
const PASSWORD = process.env.PANEL_PASSWORD ?? "";

test.describe("operator invitations", () => {
  // The acceptance screen is outside the session gate. A visitor with a bad
  // link must land on it — not on sign-in — and be told the link is no good.
  test("a bad invitation link is explained, not redirected", async ({ page }) => {
    await page.goto("/admin/invite/not-a-real-invitation-token");
    await expect(page).toHaveURL(/\/admin\/invite\//);
    await expect(page.getByRole("alert").first()).toBeVisible();
    await expect(page.locator("body")).not.toContainText(MISSING_MESSAGE_MARKER);
  });

  // The whole path, once: the owner invites, a second browser redeems the link,
  // and the new operator lands signed in. Chromium only, because sign-in is
  // rate limited per account and the journey spec already spends that budget
  // on every project.
  test("an owner invites an operator who accepts and is signed in", async ({
    browser,
    page,
  }, testInfo) => {
    test.skip(!EMAIL || !PASSWORD, "no seeded operator: set PANEL_EMAIL and PANEL_PASSWORD");
    test.skip(testInfo.project.name !== "chromium", "runs once, on chromium");

    await page.goto("/admin/login");
    await typeInto(page.locator('input[type="email"]'), EMAIL);
    await typeInto(page.locator('input[type="password"]'), PASSWORD);
    await page.click('button[type="submit"]');
    await expect(page).not.toHaveURL(/\/admin\/login/, { timeout: 20_000 });

    await page.goto("/admin/operators");
    await page.getByRole("button", { name: /invite operator|пригласить оператора/i }).click();
    const dialog = page.getByRole("dialog");
    const invitee = `invitee-${Date.now()}@omniflow.test`;
    await typeInto(dialog.locator('input[type="email"]'), invitee);
    await dialog.locator('button[type="submit"]').click();

    const linkField = dialog.locator("input[readonly]");
    await expect(linkField).toHaveValue(/\/admin\/invite\//);
    const link = await linkField.inputValue();
    await dialog.getByRole("button", { name: /^done$|^готово$/i }).click();
    await expect(page.getByText(invitee)).toBeVisible();

    const fresh = await browser.newContext();
    const guest = await fresh.newPage();
    await guest.goto(link);
    await expect(guest.getByText(invitee)).toBeVisible();
    await typeInto(guest.locator('input[autocomplete="name"]'), "Invited Operator");
    const passwords = guest.locator('input[type="password"]');
    await typeInto(passwords.nth(0), "an invited operator passphrase");
    await typeInto(passwords.nth(1), "an invited operator passphrase");
    await guest.locator('button[type="submit"]').click();

    await guest
      .getByRole("button", { name: /go to the panel|перейти в админку/i })
      .click({ timeout: 20_000 });
    await expect(guest).toHaveURL(/\/admin(\/)?$/);
    await expect(guest.locator("main")).toBeVisible();

    // The link is single-use.
    await guest.goto(link);
    await expect(guest.getByRole("alert").first()).toBeVisible();
    await fresh.close();
  });
});
