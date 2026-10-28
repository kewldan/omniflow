/// <reference types="bun" />
import { describe, expect, test } from "bun:test";

import { isCommerceKey, nextPollDelay, POLLING } from "./polling";

describe("nextPollDelay", () => {
  test("starts short and grows with the wait", () => {
    expect(nextPollDelay(0)).toBe(3_000);
    expect(nextPollDelay(20_000)).toBe(3_000);
    expect(nextPollDelay(60_000)).toBe(6_000);
    expect(nextPollDelay(5 * 60_000)).toBe(30_000);
    expect(nextPollDelay(14 * 60_000)).toBe(30_000);
  });

  test("stops at the ceiling", () => {
    expect(nextPollDelay(POLLING.ceilingMs)).toBe(0);
    expect(nextPollDelay(POLLING.ceilingMs + 1)).toBe(0);
  });

  test("never polls faster than the floor", () => {
    for (let elapsed = 0; elapsed < POLLING.ceilingMs; elapsed += 7_919) {
      expect(nextPollDelay(elapsed)).toBeGreaterThanOrEqual(POLLING.initialMs);
      expect(nextPollDelay(elapsed)).toBeLessThanOrEqual(POLLING.maxMs);
    }
  });

  test("the slow notice comes well before polling stops", () => {
    expect(POLLING.slowAfterMs).toBeLessThan(POLLING.ceilingMs);
  });
});

describe("isCommerceKey", () => {
  test("matches the views money can change", () => {
    expect(isCommerceKey("/v1/account/orders?limit=20")).toBe(true);
    expect(isCommerceKey("/v1/account/orders/abc")).toBe(true);
    expect(isCommerceKey("/v1/account/wallet?limit=20")).toBe(true);
    expect(isCommerceKey("/v1/account/subscriptions")).toBe(true);
  });

  test("leaves the rest alone", () => {
    expect(isCommerceKey("/v1/account/me")).toBe(false);
    expect(isCommerceKey("/v1/account/support/tickets")).toBe(false);
    expect(isCommerceKey(null)).toBe(false);
  });
});
