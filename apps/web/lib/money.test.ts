/// <reference types="bun" />
import { describe, expect, test } from "bun:test";

import {
  currencyExponent,
  formatMoney,
  minorToInput,
  parseMajorToMinor,
  toMajorUnits,
} from "./money";

const nbsp = " ";

describe("currencyExponent", () => {
  test.each([
    ["RUB", 2],
    ["usd", 2],
    ["EUR", 2],
    ["XTR", 0],
    ["JPY", 0],
    ["KRW", 0],
    ["VND", 0],
    ["CLP", 0],
    ["ISK", 0],
    ["UGX", 0],
    ["KWD", 3],
    ["BHD", 3],
    ["OMR", 3],
    ["JOD", 3],
    ["TND", 3],
  ])("%s has %i decimals", (currency, exponent) => {
    expect(currencyExponent(currency)).toBe(exponent);
  });

  test("an unknown code falls back to two rather than throwing", () => {
    expect(currencyExponent("not a code")).toBe(2);
  });
});

describe("formatMoney", () => {
  test("uses each currency's own exponent", () => {
    expect(formatMoney(1234, "KWD", "en")).toBe(`KWD${nbsp}1.234`);
    expect(formatMoney(1234, "BHD", "en")).toBe(`BHD${nbsp}1.234`);
    expect(formatMoney(1234, "VND", "en")).toBe("₫1,234");
    expect(formatMoney(1234, "JPY", "en")).toBe("¥1,234");
    expect(formatMoney(1234, "USD", "en")).toBe("$12.34");
    expect(formatMoney(150000, "RUB", "ru")).toBe(`1${nbsp}500,00${nbsp}₽`);
  });

  test("renders Telegram Stars as whole stars", () => {
    expect(formatMoney(1200, "XTR", "en")).toBe(`⭐${nbsp}1,200`);
    expect(formatMoney(1200, "xtr", "en")).toBe(`⭐${nbsp}1,200`);
  });

  test("the customer panel's narrow symbol", () => {
    expect(formatMoney(1234, "USD", "en", { display: "narrowSymbol" })).toBe("$12.34");
    expect(formatMoney(1234, "CAD", "en", { display: "narrowSymbol" })).toBe("$12.34");
    expect(formatMoney(1234, "CAD", "en")).toBe("CA$12.34");
  });

  test("compact form for a chart axis", () => {
    expect(formatMoney(123_456_700, "USD", "en", { compact: true })).toBe("$1.2M");
  });

  test("a code the runtime rejects still renders", () => {
    expect(formatMoney(1200, "1X", "en")).toBe(`12.00${nbsp}1X`);
  });
});

describe("parseMajorToMinor", () => {
  test("converts with each currency's exponent", () => {
    expect(parseMajorToMinor("4.99", "USD")).toEqual({ ok: true, minor: 499 });
    expect(parseMajorToMinor("4,99", "RUB")).toEqual({ ok: true, minor: 499 });
    expect(parseMajorToMinor("1 200", "RUB")).toEqual({ ok: true, minor: 120000 });
    expect(parseMajorToMinor("1.234", "KWD")).toEqual({ ok: true, minor: 1234 });
    expect(parseMajorToMinor("1.2", "KWD")).toEqual({ ok: true, minor: 1200 });
    expect(parseMajorToMinor("500", "XTR")).toEqual({ ok: true, minor: 500 });
    expect(parseMajorToMinor("25000", "VND")).toEqual({ ok: true, minor: 25000 });
    expect(parseMajorToMinor("0.1", "USD")).toEqual({ ok: true, minor: 10 });
    expect(parseMajorToMinor("19.", "USD")).toEqual({ ok: true, minor: 1900 });
  });

  test("does not lose a cent to floating point", () => {
    // 1.005 * 100 is 100.49999… in binary floating point.
    expect(parseMajorToMinor("1.01", "USD")).toEqual({ ok: true, minor: 101 });
    expect(parseMajorToMinor("0.29", "USD")).toEqual({ ok: true, minor: 29 });
    expect(parseMajorToMinor("4.35", "USD")).toEqual({ ok: true, minor: 435 });
  });

  test("refuses rather than rounds", () => {
    expect(parseMajorToMinor("4.999", "USD")).toEqual({ ok: false, reason: "precision" });
    expect(parseMajorToMinor("4.5", "XTR")).toEqual({ ok: false, reason: "precision" });
    expect(parseMajorToMinor("4.5", "JPY")).toEqual({ ok: false, reason: "precision" });
  });

  test("names what is wrong", () => {
    expect(parseMajorToMinor("", "USD")).toEqual({ ok: false, reason: "empty" });
    expect(parseMajorToMinor("  ", "USD")).toEqual({ ok: false, reason: "empty" });
    expect(parseMajorToMinor("abc", "USD")).toEqual({ ok: false, reason: "invalid" });
    expect(parseMajorToMinor("1e3", "USD")).toEqual({ ok: false, reason: "invalid" });
    expect(parseMajorToMinor("1.2.3", "USD")).toEqual({ ok: false, reason: "invalid" });
    expect(parseMajorToMinor("-5", "USD")).toEqual({ ok: false, reason: "negative" });
    expect(parseMajorToMinor("99999999999999999", "USD")).toEqual({
      ok: false,
      reason: "tooLarge",
    });
  });

  test("accepts a negative amount only when asked to", () => {
    expect(parseMajorToMinor("-5.25", "USD", { allowNegative: true })).toEqual({
      ok: true,
      minor: -525,
    });
  });
});

describe("minorToInput", () => {
  test("is the inverse of parsing", () => {
    expect(minorToInput(499, "USD")).toBe("4.99");
    expect(minorToInput(5, "USD")).toBe("0.05");
    expect(minorToInput(1234, "KWD")).toBe("1.234");
    expect(minorToInput(500, "XTR")).toBe("500");
    expect(minorToInput(-525, "USD")).toBe("-5.25");
    for (const [minor, currency] of [
      [123456, "RUB"],
      [7, "BHD"],
      [0, "USD"],
    ] as const) {
      expect(parseMajorToMinor(minorToInput(minor, currency), currency)).toEqual({
        ok: true,
        minor,
      });
    }
  });

  test("toMajorUnits divides by the right power of ten", () => {
    expect(toMajorUnits(1234, "KWD")).toBe(1.234);
    expect(toMajorUnits(1234, "VND")).toBe(1234);
  });
});
