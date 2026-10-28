/// <reference types="bun" />
import { describe, expect, test } from "bun:test";

import { safeAdminNext, safeNext, signInPath } from "./sign-in-path";

const hostile = [
  "https://evil.example/admin",
  "//evil.example/admin",
  "/\\evil.example",
  "/admin/\\..\\evil",
  "/\tadmin",
  "/admin\n//evil.example",
  "javascript:alert(1)",
  "admin",
  "",
];

describe("safeAdminNext", () => {
  test("keeps a page inside the operator panel", () => {
    expect(safeAdminNext("/admin")).toBe("/admin");
    expect(safeAdminNext("/admin/customers?status=active")).toBe("/admin/customers?status=active");
    expect(safeAdminNext("/admin/finance/123#refunds")).toBe("/admin/finance/123#refunds");
  });

  test("falls back to the panel home for an absent value", () => {
    expect(safeAdminNext(null)).toBe("/admin");
    expect(safeAdminNext(undefined)).toBe("/admin");
  });

  test.each(hostile)("refuses %p", (value) => {
    expect(safeAdminNext(value)).toBe("/admin");
  });

  test("refuses a path outside the panel", () => {
    expect(safeAdminNext("/account/wallet")).toBe("/admin");
    expect(safeAdminNext("/administrator")).toBe("/admin");
  });

  test("never sends an operator back to a sign-in screen", () => {
    expect(safeAdminNext("/admin/login")).toBe("/admin");
    expect(safeAdminNext("/admin/login?next=/admin/x")).toBe("/admin");
    expect(safeAdminNext("/admin/setup")).toBe("/admin");
  });
});

describe("safeNext", () => {
  test("keeps a page inside the customer panel", () => {
    expect(safeNext("/account/orders/1")).toBe("/account/orders/1");
  });

  test.each(hostile)("refuses %p", (value) => {
    expect(safeNext(value)).toBe("/account");
  });

  test("refuses the operator panel and the sign-in screen", () => {
    expect(safeNext("/admin")).toBe("/account");
    expect(safeNext("/account/sign-in?next=/account/x")).toBe("/account");
    expect(safeNext("/accounting")).toBe("/account");
  });

  test("signInPath carries only a safe destination", () => {
    expect(signInPath("/account/wallet")).toBe("/account/sign-in?next=%2Faccount%2Fwallet");
    expect(signInPath("https://evil.example")).toBe("/account/sign-in");
  });
});
