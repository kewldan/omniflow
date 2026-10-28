/// <reference types="bun" />
import { describe, expect, test } from "bun:test";

import { signedOutRedirect } from "./proxy";

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

  test("does not mistake a lookalike prefix for a panel", () => {
    expect(signedOutRedirect("/accounting", "", none)).toBeNull();
    expect(signedOutRedirect("/administrator", "", none)).toBeNull();
  });
});
