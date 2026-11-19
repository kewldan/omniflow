/// <reference types="bun" />
import { describe, expect, test } from "bun:test";

import { isLocale, LOCALE_COOKIE, localeCookie, negotiateLocale, resolveLocale } from "./locale";

describe("negotiateLocale", () => {
  test.each([
    ["ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7", "ru"],
    ["en-GB,en;q=0.9,ru;q=0.8", "en"],
    ["de-DE,de;q=0.9,ru;q=0.5,en;q=0.4", "ru"],
    ["en;q=0.4,ru;q=0.6", "ru"],
    ["RU", "ru"],
    ["fr, ru", "ru"],
  ])("%s → %s", (header, expected) => {
    expect(negotiateLocale(header)).toBe(expected as "ru" | "en");
  });

  test("keeps the browser's order between equal weights", () => {
    expect(negotiateLocale("en, ru")).toBe("en");
    expect(negotiateLocale("ru, en")).toBe("ru");
  });

  test("a language refused with q=0 is never chosen", () => {
    expect(negotiateLocale("ru;q=0, de")).toBeNull();
  });

  test("nothing usable is null", () => {
    expect(negotiateLocale("")).toBeNull();
    expect(negotiateLocale(undefined)).toBeNull();
    expect(negotiateLocale("de-DE,fr;q=0.8")).toBeNull();
    expect(negotiateLocale("*")).toBeNull();
    expect(negotiateLocale("ru;q=abc")).toBeNull();
  });
});

describe("resolveLocale", () => {
  test("an explicit locale wins over everything", () => {
    expect(resolveLocale({ acceptLanguage: "en", cookie: "en", requested: "ru" })).toBe("ru");
  });

  test("the cookie wins over the browser's language", () => {
    expect(resolveLocale({ acceptLanguage: "ru-RU", cookie: "en" })).toBe("en");
    expect(resolveLocale({ acceptLanguage: "en-US", cookie: "ru" })).toBe("ru");
  });

  test("an unknown cookie value is ignored, not trusted", () => {
    expect(resolveLocale({ acceptLanguage: "ru-RU", cookie: "de" })).toBe("ru");
    expect(resolveLocale({ acceptLanguage: "ru-RU", cookie: "../../en" })).toBe("ru");
  });

  test("with nothing to go on it is English", () => {
    expect(resolveLocale({})).toBe("en");
    expect(resolveLocale({ acceptLanguage: "de" })).toBe("en");
  });
});

describe("localeCookie", () => {
  test("is a site-wide, long-lived, lax cookie holding only the code", () => {
    const cookie = localeCookie("ru");
    expect(cookie.startsWith(`${LOCALE_COOKIE}=ru;`)).toBe(true);
    expect(cookie).toContain("Path=/");
    expect(cookie).toContain("SameSite=Lax");
    expect(cookie).toMatch(/Max-Age=\d+/);
    expect(cookie).not.toContain("Secure");
    expect(localeCookie("en", true)).toContain("Secure");
  });
});

describe("isLocale", () => {
  test("accepts exactly the two catalogues", () => {
    expect(isLocale("ru")).toBe(true);
    expect(isLocale("en")).toBe(true);
    expect(isLocale("RU")).toBe(false);
    expect(isLocale(undefined)).toBe(false);
  });
});
