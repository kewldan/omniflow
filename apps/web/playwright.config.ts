import { defineConfig, devices } from "@playwright/test";

/**
 * End-to-end coverage of the admin panel.
 *
 * The suite is deliberately small and aimed at the journeys where a regression
 * is expensive rather than merely visible: signing in, being refused what you
 * are not entitled to, and the accessibility and localisation gates that a
 * component test cannot see because they are properties of the rendered page.
 *
 * It runs against a built application rather than the dev server. Next.js
 * behaves differently in development — different error overlays, different
 * hydration timing — and a gate that passes against a build nobody ships is not
 * a gate.
 */
export default defineConfig({
  testDir: "./e2e",
  // A flaky end-to-end suite gets disabled within a month, so a failure is a
  // failure: no retries locally, one in CI for genuine network flakes.
  retries: process.env.CI ? 1 : 0,
  // Serial locally so a failure is readable; parallel in CI where time costs.
  workers: process.env.CI ? undefined : 1,
  forbidOnly: Boolean(process.env.CI),
  reporter: process.env.CI ? [["github"], ["html", { open: "never" }]] : [["list"]],
  timeout: 30_000,
  expect: { timeout: 10_000 },

  use: {
    baseURL: process.env.PLAYWRIGHT_BASE_URL ?? "http://127.0.0.1:3000",
    // A trace only on the first retry: they are large, and the run that
    // matters is the one that failed twice.
    trace: "on-first-retry",
    screenshot: "only-on-failure",
  },

  projects: [
    // Chromium and WebKit cover the two engines an operator plausibly uses.
    // Firefox is omitted rather than pretended: adding it without anybody
    // reading its failures would be a badge, not a gate.
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "webkit", use: { ...devices["Desktop Safari"] } },
    // One mobile viewport, because the panel is used from a phone during an
    // incident and that is exactly when a broken layout costs something.
    { name: "mobile", use: { ...devices["Pixel 7"] } },
  ],

  webServer: process.env.PLAYWRIGHT_BASE_URL
    ? undefined
    : {
        command: "bun run start",
        url: "http://127.0.0.1:3000",
        reuseExistingServer: !process.env.CI,
        timeout: 120_000,
      },
});
