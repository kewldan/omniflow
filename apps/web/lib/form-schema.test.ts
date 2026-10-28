/// <reference types="bun" />
import { describe, expect, test } from "bun:test";

import {
  decodeIssue,
  issue,
  moneyIssue,
  priceList,
  readPriceList,
  wholeNumber,
} from "./form-schema";

function messages(result: {
  success: boolean;
  error?: { issues: { message: string; path: PropertyKey[] }[] };
}) {
  return (result.error?.issues ?? []).map((entry) => ({
    message: entry.message,
    path: entry.path.join("."),
  }));
}

describe("issue", () => {
  test("round-trips a key with values", () => {
    expect(decodeIssue(issue("min", { min: 3 }))).toEqual({ key: "min", values: { min: 3 } });
    expect(decodeIssue(issue("required"))).toEqual({ key: "required" });
  });
});

describe("priceList", () => {
  const schema = priceList();

  test("accepts major-unit prices in several currencies", () => {
    const rows = [
      { amount: "499.00", currency: "rub" },
      { amount: "4.99", currency: "USD" },
      { amount: "1.250", currency: "KWD" },
      { amount: "250", currency: "XTR" },
    ];
    expect(schema.safeParse(rows).success).toBe(true);
    expect(readPriceList(rows)).toEqual({ KWD: 1250, RUB: 49900, USD: 499, XTR: 250 });
  });

  test("reports each bad row on the row, instead of dropping it", () => {
    const result = schema.safeParse([
      { amount: "4.999", currency: "USD" },
      { amount: "abc", currency: "EUR" },
      { amount: "1", currency: "RUBLES" },
      { amount: "2", currency: "usd" },
      { amount: "", currency: "GBP" },
    ]);
    expect(result.success).toBe(false);
    expect(messages(result)).toEqual([
      { message: issue("money.precision", { decimals: 2 }), path: "0.amount" },
      { message: issue("money.invalid"), path: "1.amount" },
      { message: issue("currency"), path: "2.currency" },
      { message: issue("duplicateCurrency"), path: "3.currency" },
      { message: issue("money.empty"), path: "4.amount" },
    ]);
  });

  test("refuses an empty list", () => {
    expect(messages(schema.safeParse([]))).toEqual([
      { message: issue("pricesRequired"), path: "" },
    ]);
  });
});

describe("wholeNumber", () => {
  test("checks the bounds and the shape", () => {
    const ttl = wholeNumber({ max: 3600, min: 30 });
    expect(ttl.safeParse("300").success).toBe(true);
    expect(messages(ttl.safeParse("10"))[0]?.message).toBe(issue("min", { min: 30 }));
    expect(messages(ttl.safeParse("4000"))[0]?.message).toBe(issue("max", { max: 3600 }));
    expect(messages(ttl.safeParse("4.5"))[0]?.message).toBe(issue("integer"));
    expect(messages(ttl.safeParse(""))[0]?.message).toBe(issue("required"));
  });

  test("an optional field may be empty", () => {
    expect(wholeNumber({ optional: true }).safeParse("").success).toBe(true);
  });
});

describe("moneyIssue", () => {
  test("states the limits in the reader's terms", () => {
    const format = (minor: number) => `$${(minor / 100).toFixed(2)}`;
    expect(moneyIssue("0.50", "USD", { formatted: format, minimumMinor: 100 })).toBe(
      issue("money.min", { min: "$1.00" }),
    );
    expect(moneyIssue("600", "USD", { formatted: format, maximumMinor: 50000 })).toBe(
      issue("money.max", { max: "$500.00" }),
    );
    expect(moneyIssue("4.99", "USD")).toBeNull();
  });
});
