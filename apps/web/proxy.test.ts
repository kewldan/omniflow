/// <reference types="bun" />
import { describe, expect, test } from "bun:test";

import { legacyRedirect, signedOutRedirect } from "./proxy";

const none = { account: false, admin: false };

describe("signedOutRedirect", () => {
  test("sends a signed-out customer to sign-in, remembering where they were", () => {
    expect(signedOutRedirect("/account/wallet", "?tab=history", none)).toBe(
      `/account/sign-in?next=${encodeURIComponent("/account/wallet?tab=history")}`,
    );
  });

  test("does not carry next for the dashboard itself", () => {
    expect(signedOutRedirect("/account", "", none)).toBe("/account/sign-in");
  });

  test("leaves every sign-in screen open", () => {
    expect(signedOutRedirect("/account/sign-in", "?ref=abc", none)).toBeNull();
    expect(signedOutRedirect("/account/sign-in/link", "?token=t", none)).toBeNull();
  });

  test("lets a customer with a session cookie through", () => {
    expect(signedOutRedirect("/account/orders", "", { account: true, admin: false })).toBeNull();
  });

  test("an operator cookie does not open the customer panel", () => {
    expect(
      signedOutRedirect("/account/orders", "", { account: false, admin: true }),
    ).not.toBeNull();
  });

  test("a customer cookie does not open the operator panel", () => {
    expect(signedOutRedirect("/admin/customers", "", { account: true, admin: false })).toBe(
      `/admin/login?next=${encodeURIComponent("/admin/customers")}`,
    );
  });

  test("leaves the operator sign-in and setup screens open", () => {
    expect(signedOutRedirect("/admin/login", "", none)).toBeNull();
    expect(signedOutRedirect("/admin/setup", "", none)).toBeNull();
  });

  test("leaves an invitation link open to somebody with no account yet", () => {
    expect(signedOutRedirect("/admin/invite/some-token", "", none)).toBeNull();
    expect(signedOutRedirect("/admin/invitations", "", none)).toBe(
      "/admin/login?next=%2Fadmin%2Finvitations",
    );
  });

  test("does not mistake a lookalike prefix for a panel", () => {
    expect(signedOutRedirect("/accounting", "", none)).toBeNull();
    expect(signedOutRedirect("/administrator", "", none)).toBeNull();
  });
});

describe("legacyRedirect", () => {
  test("sends the previous cabinet's paths to the account", () => {
    expect(legacyRedirect("/login", "?ref=ABC")).toBe("/account/sign-in?ref=ABC");
    expect(legacyRedirect("/subscription", "")).toBe("/account/subscriptions");
    expect(legacyRedirect("/referral", "")).toBe("/account/referrals");
    expect(legacyRedirect("/subscription/123", "")).toBe("/account/subscriptions");
  });

  test("carries a gift code into the wallet's redeem form", () => {
    expect(legacyRedirect("/buy/gift/GIFT-42", "")).toBe("/account/wallet?code=GIFT-42");
    expect(legacyRedirect("/gift/abc", "?utm=x")).toBe("/account/wallet?utm=x&code=abc");
  });

  test("an unknown path stays a 404 unless a fallback is set", () => {
    expect(legacyRedirect("/cabinet/whatever", "")).toBeNull();
    expect(legacyRedirect("/cabinet/whatever", "", {}, "/account")).toBe("/account");
  });

  test("leaves Omniflow's own paths and files alone", () => {
    for (const path of [
      "/",
      "/account/wallet",
      "/admin/login",
      "/guest/x",
      "/pages/terms",
      "/status",
    ]) {
      expect(legacyRedirect(path, "")).toBeNull();
    }
    expect(legacyRedirect("/manifest.webmanifest", "")).toBeNull();
    expect(legacyRedirect("/apay-meta-file.txt", "")).toBeNull();
    expect(legacyRedirect("/accounting", "", {}, "/account")).toBe("/account");
    expect(legacyRedirect("/account/wallet", "", {}, "/account")).toBeNull();
  });

  test("an operator's table extends the built-in one, and the fallback can be turned off", () => {
    expect(legacyRedirect("/old-news", "", { "/old-news": "/account/news" })).toBe("/account/news");
    expect(legacyRedirect("/elsewhere", "", {}, "off")).toBeNull();
  });

  test("never redirects to another origin", () => {
    expect(legacyRedirect("/evil", "", { "/evil": "//attacker.example" }, "off")).toBeNull();
    expect(legacyRedirect("/evil", "", { "/evil": "https://attacker.example" }, "off")).toBeNull();
  });
});
