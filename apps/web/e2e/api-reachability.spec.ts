import { expect, test } from "@playwright/test";

/**
 * The browser can reach the API on its own origin.
 *
 * Both panels call the API same-origin: the shared client in
 * `packages/api-client/src/fetcher.ts` keeps a module-level base URL of `""`
 * and nothing calls `setBaseUrl`, so every request from the browser goes to the
 * web origin and expects the API to answer it. Something has to make that true
 * — the reverse proxy in production, the middleware when nothing is in front of
 * the stack.
 *
 * When nothing does, the panels still render, still pass every accessibility
 * and layout gate, and still refuse anonymous visitors correctly. They simply
 * cannot complete a single request: sign-in reports "Sign-in failed. Try
 * again." with no clue that the call never reached the API at all. That is the
 * shape of failure this file exists to catch, and it is why the assertions are
 * about transport rather than about any screen.
 */

// Two unauthenticated endpoints, one per surface, chosen because they answer
// without a session: a 404 here means the request never reached the Go API.
const PUBLIC_ENDPOINTS = [
  { path: "/v1/panel/bootstrap", surface: "operator panel" },
  { path: "/v1/account/auth/methods", surface: "customer panel" },
];

test.describe("browser-to-API reachability", () => {
  for (const { path, surface } of PUBLIC_ENDPOINTS) {
    test(`${path} is served from the web origin for the ${surface}`, async ({ request }) => {
      const response = await request.get(path);
      expect(
        response.status(),
        `${path} answered ${response.status()}. A 404 means the web origin does not route ` +
          "/v1 to the API, so every call the panel makes from the browser will fail. " +
          "Route /v1 in the reverse proxy, or set API_INTERNAL_URL on the web service.",
      ).toBe(200);
      expect(response.headers()["content-type"]).toContain("json");
    });
  }

  // The session cookie is only first-party — and therefore only sent at all —
  // because the API answers on the web's own origin. A deployment that moved
  // the API cross-origin would need CORS and SameSite=None, so this asserts the
  // property that keeps the current cookie model workable.
  test("the API answers on the same origin as the page", async ({ page, request }) => {
    await page.goto("/admin/login");
    const pageOrigin = new URL(page.url()).origin;
    const response = await request.get("/v1/panel/bootstrap");
    expect(new URL(response.url()).origin).toBe(pageOrigin);
  });
});
