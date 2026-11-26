import { describe, expect, test } from "bun:test";

import { operatorReasonHeaders } from "./operator-reason";

/** What `new Headers()` accepts: Latin-1 only, as in every browser. */
function latin1(value: string): boolean {
  return [...value].every((character) => (character.codePointAt(0) ?? 0) <= 0xff);
}

describe("operatorReasonHeaders", () => {
  test("a Russian reason becomes a header the browser will send", () => {
    const headers = operatorReasonHeaders("Нарушение правил: мультиаккаунт");
    expect(latin1(headers["X-Operator-Reason"] ?? "")).toBe(true);
    expect(() => new Headers(headers)).not.toThrow();
    expect(decodeURIComponent(headers["X-Operator-Reason"] ?? "")).toBe(
      "Нарушение правил: мультиаккаунт",
    );
    expect(headers["X-Operator-Reason-Encoding"]).toBe("percent");
  });

  test("every script the panel is translated into survives", () => {
    for (const reason of ["已封禁：小号", "مسدود شده", "Заблоковано", "50% refund, as agreed"]) {
      const headers = operatorReasonHeaders(reason);
      expect(() => new Headers(headers)).not.toThrow();
      expect(decodeURIComponent(headers["X-Operator-Reason"] ?? "")).toBe(reason);
    }
  });
});
