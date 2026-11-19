/// <reference types="bun" />
import { describe, expect, test } from "bun:test";

import { localized } from "./localized";

describe("localized", () => {
  test("uses the half that matches the interface language", () => {
    expect(localized("ru", "Billing", "Оплата")).toBe("Оплата");
    expect(localized("en", "Billing", "Оплата")).toBe("Billing");
  });

  test("falls back to the other language when the matching half is empty", () => {
    expect(localized("ru", "Billing", "")).toBe("Billing");
    expect(localized("en", "  ", "Оплата")).toBe("Оплата");
    expect(localized("ru", "Billing", undefined)).toBe("Billing");
  });

  test("falls back to the code when neither half was written", () => {
    expect(localized("ru", "", "", "billing")).toBe("billing");
    expect(localized("en", null, undefined, "billing")).toBe("billing");
    expect(localized("en", "", "")).toBe("");
  });

  test("treats an unknown locale as English", () => {
    expect(localized("de", "Billing", "Оплата")).toBe("Billing");
  });
});
