import { createHash, createHmac } from "node:crypto";

import { type BrowserContext, expect, type Page, test } from "@playwright/test";

/**
 * The customer flows the journeys cannot reach on an installation with no
 * Remnawave and nothing bought: sign-in the way Telegram really hands it over,
 * the magic-link confirmation, an order moving through provisioning, a wait
 * that runs long, and the error and empty states of the lists.
 *
 * The sign-in tests go to the real API, because the signature check and the
 * cookie are the point. Everything else stubs the API with `page.route`: the
 * edge gate only needs a session cookie to be present, so a placeholder cookie
 * plus a stubbed `/v1/account/me` puts the browser in a signed-in shell with
 * whatever order, list, or failure the test needs — states a real stack would
 * take a payment provider and a provisioning worker to produce.
 */

const BOT_TOKEN = process.env.CUSTOMER_BOT_TOKEN ?? "";
const TELEGRAM_ID = Number(process.env.CUSTOMER_TELEGRAM_ID ?? "770002");
const ORDER_ID = "7d1e4a52-0000-4000-8000-000000000001";
const SUBSCRIPTION_ID = "7d1e4a52-0000-4000-8000-000000000002";

function problem(status: number, code: string) {
  return {
    body: JSON.stringify({
      status,
      title: code,
      type: `https://omniflow.dev/problems/${code}`,
    }),
    contentType: "application/problem+json",
    status,
  };
}

/** A browser that looks signed in to the edge gate and the shell, with no API behind it. */
async function signedInShell(page: Page, context: BrowserContext, baseURL: string) {
  await context.addCookies([{ name: "omniflow_account", url: baseURL, value: "stub" }]);
  // Registered first, so every specific route below takes precedence.
  await page.route("**/v1/**", (route) => route.fulfill(problem(404, "not_found")));
  await page.route("**/v1/account/me", (route) =>
    route.fulfill({
      body: JSON.stringify({
        customer: { id: "c-1", locale: "en", status: "active", timezone: "Europe/Moscow" },
        session: {
          authMethod: "telegram",
          expiresAt: new Date(Date.now() + 86_400_000).toISOString(),
          id: "s-1",
          reauthenticationRequired: false,
        },
      }),
      contentType: "application/json",
    }),
  );
}

function order(phase: string, overrides: Record<string, unknown> = {}) {
  return {
    createdAt: new Date().toISOString(),
    currency: "RUB",
    discountMinor: 0,
    externalMinor: 0,
    id: ORDER_ID,
    operation: "purchase",
    paidMinor: 49_900,
    phase,
    plan: "Standard",
    refundedMinor: 0,
    state: phase === "completed" ? "completed" : "paid",
    subscriptionId: SUBSCRIPTION_ID,
    subtotalMinor: 49_900,
    walletMinor: 0,
    ...overrides,
  };
}

test.describe("customer flows with a stubbed API", () => {
  test("an order being set up updates itself when provisioning finishes", async ({
    page,
    context,
    baseURL,
  }) => {
    await signedInShell(page, context, baseURL ?? "");
    let reads = 0;
    await page.route(`**/v1/account/orders/${ORDER_ID}`, (route) => {
      reads += 1;
      return route.fulfill({
        body: JSON.stringify(order(reads < 2 ? "provisioning" : "completed")),
        contentType: "application/json",
      });
    });

    await page.goto(`/account/orders/${ORDER_ID}`);
    await expect(
      page.getByText("Your subscription is being set up.", { exact: false }),
    ).toBeVisible();
    // No reload: the bounded poll reads the order again and the page follows.
    await expect(
      page.getByText("Everything is paid for and set up.", { exact: false }),
    ).toBeVisible({
      timeout: 15_000,
    });
  });

  test("a wait that runs long says so and offers support", async ({ page, context, baseURL }) => {
    await page.clock.install();
    await signedInShell(page, context, baseURL ?? "");
    await page.route(`**/v1/account/orders/${ORDER_ID}`, (route) =>
      route.fulfill({
        body: JSON.stringify(order("provisioning")),
        contentType: "application/json",
      }),
    );

    await page.goto(`/account/orders/${ORDER_ID}`);
    await expect(
      page.getByText("Your subscription is being set up.", { exact: false }),
    ).toBeVisible();
    await expect(page.getByText(/taking longer than usual/i)).toHaveCount(0);

    await page.clock.fastForward("02:30");
    await expect(page.getByText(/taking longer than usual/i)).toBeVisible();
    await expect(page.getByRole("link", { name: /contact support/i })).toHaveAttribute(
      "href",
      "/account/support/new",
    );
    await expect(page.getByText(ORDER_ID)).toBeVisible();
  });

  test("the connect screen explains a subscription still being provisioned", async ({
    page,
    context,
    baseURL,
  }) => {
    await signedInShell(page, context, baseURL ?? "");
    await page.route(`**/v1/account/subscriptions/${SUBSCRIPTION_ID}/connection**`, (route) =>
      route.fulfill(problem(409, "provisioning")),
    );
    await page.goto(`/account/subscriptions/${SUBSCRIPTION_ID}/connect`);
    await expect(page.getByText("Still being set up")).toBeVisible();
  });

  test("the order list has an empty state and an error state", async ({
    page,
    context,
    baseURL,
  }) => {
    await signedInShell(page, context, baseURL ?? "");
    let fail = false;
    await page.route("**/v1/account/orders?**", (route) =>
      fail
        ? route.fulfill(problem(500, "internal"))
        : route.fulfill({ body: JSON.stringify({ items: [] }), contentType: "application/json" }),
    );

    await page.goto("/account/orders");
    await expect(page.getByText("No orders yet")).toBeVisible();

    fail = true;
    await page.reload();
    await expect(page.getByText("No orders yet")).toHaveCount(0);
    await expect(
      page
        .getByRole("alert")
        .or(page.getByText(/could not|не удалось/i))
        .first(),
    ).toBeVisible();
  });
});

test.describe("magic-link confirmation", () => {
  test("the token leaves the address bar and is posted only on the customer's click", async ({
    page,
  }) => {
    const posted: { body: string; type: string | undefined }[] = [];
    await page.route("**/v1/account/auth/link", (route) => {
      posted.push({
        body: route.request().postData() ?? "",
        type: route.request().headers()["content-type"],
      });
      return route.fulfill(problem(401, "link_invalid"));
    });

    await page.goto("/account/sign-in/confirm#token=planted-token");
    await expect(page.getByRole("button", { name: /continue signing in/i })).toBeVisible();
    await expect.poll(() => page.evaluate(() => window.location.hash)).toBe("");
    expect(posted, "nothing is posted before the customer confirms").toEqual([]);

    await page.getByRole("button", { name: /continue signing in/i }).click();
    await expect(page).toHaveURL(/\/account\/sign-in\?error=link_invalid/);
    await expect(page.locator("main").getByRole("alert")).toContainText(
      /already been used|expired/i,
    );
    expect(posted).toEqual([
      { body: JSON.stringify({ token: "planted-token" }), type: "application/json" },
    ]);
  });

  test("a confirmation page with no token says the link is unusable", async ({ page }) => {
    await page.goto("/account/sign-in/confirm");
    await expect(page.getByRole("link", { name: /back to sign-in/i })).toBeVisible();
  });
});

/**
 * Builds the payload the login widget hands its `data-onauth` callback, with
 * every optional field a real account carries, signed the widget's way.
 */
function widgetPayload(): Record<string, string | number> {
  const fields: Record<string, string | number> = {
    auth_date: Math.floor(Date.now() / 1000),
    first_name: "Widget",
    id: TELEGRAM_ID,
    last_name: "Customer",
    photo_url: "https://t.me/i/userpic/320/widget.jpg",
    username: "widget_customer",
  };
  const check = Object.keys(fields)
    .sort()
    .map((key) => `${key}=${fields[key]}`)
    .join("\n");
  const secret = createHash("sha256").update(BOT_TOKEN).digest();
  return { ...fields, hash: createHmac("sha256", secret).update(check).digest("hex") };
}

/** Builds Mini App initData, signed under HMAC("WebAppData", token) as Telegram does. */
function miniAppInitData(): string {
  const fields: Record<string, string> = {
    auth_date: String(Math.floor(Date.now() / 1000)),
    query_id: "AAE2e",
    user: JSON.stringify({
      first_name: "Mini",
      id: TELEGRAM_ID + 1,
      language_code: "en",
      last_name: "App",
      username: "mini_app_customer",
    }),
  };
  const check = Object.keys(fields)
    .sort()
    .map((key) => `${key}=${fields[key]}`)
    .join("\n");
  const secret = createHmac("sha256", "WebAppData").update(BOT_TOKEN).digest();
  const hash = createHmac("sha256", secret).update(check).digest("hex");
  return new URLSearchParams({ ...fields, hash }).toString();
}

test.describe("Telegram sign-in as the browser receives it", () => {
  test.skip(
    !BOT_TOKEN,
    "no bot token: set CUSTOMER_BOT_TOKEN to the token the API is configured with",
  );

  test("the login widget's callback signs the customer in", async ({ page }) => {
    // The installation offers the widget; the widget script itself is a stub
    // that draws its frame, because a test must not load telegram.org.
    await page.route("**/v1/account/auth/methods", (route) =>
      route.fulfill({
        body: JSON.stringify({
          magicLink: false,
          oidc: [],
          telegram: true,
          telegramBot: "e2e_bot",
        }),
        contentType: "application/json",
      }),
    );
    await page.route("https://telegram.org/js/telegram-widget.js**", (route) =>
      route.fulfill({
        body: 'document.getElementById("telegram-login").appendChild(document.createElement("iframe"));',
        contentType: "text/javascript",
      }),
    );

    await page.goto("/account/sign-in");
    await expect
      .poll(() =>
        page.evaluate(
          () => typeof (window as unknown as { onTelegramAuth?: unknown }).onTelegramAuth,
        ),
      )
      .toBe("function");
    // Exactly what Telegram does: call the page's callback with the user.
    await page.evaluate((payload) => {
      (window as unknown as { onTelegramAuth: (user: unknown) => void }).onTelegramAuth(payload);
    }, widgetPayload());

    await expect(page).toHaveURL(/\/account$/, { timeout: 15_000 });
    await expect(page.getByRole("navigation", { name: /sections|разделы/i })).toBeVisible();
  });

  test("a Mini App launch signs the customer in from its initData", async ({ page }) => {
    // Telegram opens a Mini App with initData in the fragment. Opening the
    // panel itself sends a signed-out visitor through the edge gate to the
    // sign-in screen, and the fragment survives that redirect.
    await page.goto(`/account#tgWebAppData=${encodeURIComponent(miniAppInitData())}`);
    await expect(page).toHaveURL(/\/account$/, { timeout: 15_000 });
    await expect(page.getByRole("navigation", { name: /sections|разделы/i })).toBeVisible();
  });
});
