import { expect, type Locator, test } from "@playwright/test";

/** See operator-journey.spec.ts: a fill before hydration can be lost. */
async function typeInto(field: Locator, value: string): Promise<void> {
  await expect(async () => {
    await field.fill(value);
    await expect(field).toHaveValue(value, { timeout: 1000 });
  }).toPass({ timeout: 10_000 });
}

const EMAIL = process.env.PANEL_EMAIL ?? "";
const PASSWORD = process.env.PANEL_PASSWORD ?? "";

// The palette used to vanish below the md breakpoint, which is the width an
// operator answering an incident from a phone has. This runs on the mobile
// project only: sign-in is rate limited per account, and the desktop journey
// already spends that budget.
test.describe("command palette", () => {
  test("is reachable on a phone and searches records", async ({ page }, testInfo) => {
    test.skip(!EMAIL || !PASSWORD, "no seeded operator: set PANEL_EMAIL and PANEL_PASSWORD");
    test.skip(testInfo.project.name !== "mobile", "the phone layout is what this covers");

    await page.goto("/admin/login");
    await typeInto(page.locator('input[type="email"]'), EMAIL);
    await typeInto(page.locator('input[type="password"]'), PASSWORD);
    await page.click('button[type="submit"]');
    await expect(page).not.toHaveURL(/\/admin\/login/, { timeout: 20_000 });

    await page.getByRole("button", { name: /^search…$|^поиск…$/i }).click();
    const palette = page.getByRole("dialog");
    await expect(palette).toBeVisible();

    // A Telegram handle is the one thing operators try first that Omniflow
    // cannot find, and the palette must say why rather than "nothing matched".
    await palette.getByRole("combobox").fill("@somebody_here");
    await expect(palette.getByText(/usernames are not stored|не хранятся/i)).toBeVisible();

    // Screens are still found by name.
    await palette.getByRole("combobox").fill("operators");
    await expect(
      palette.getByRole("option", { name: /operators|операторы/i }).first(),
    ).toBeVisible();
  });
});
