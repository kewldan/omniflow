import { describe, expect, test } from "bun:test";

import { estimatePing } from "./ping-estimate";

describe("estimatePing", () => {
  test("TCP: two round trips to the entry, plus the tail", () => {
    // Netherlands through Poland from Moscow: 92 ms timed, 28 ms past the entry.
    expect(estimatePing(92, { tailMs: 28, transport: "tcp" })).toEqual({
      estimated: true,
      value: 72,
    });
  });

  test("UDP: the refusal is one round trip", () => {
    expect(estimatePing(45, { tailMs: 7, transport: "udp" })).toEqual({
      estimated: true,
      value: 52,
    });
  });

  test("no model or no answer passes the measurement through", () => {
    expect(estimatePing(80, undefined)).toEqual({ estimated: false, value: 80 });
    expect(estimatePing(null, { tailMs: 5, transport: "tcp" })).toEqual({
      estimated: false,
      value: null,
    });
    expect(estimatePing(undefined, undefined)).toEqual({ estimated: false, value: undefined });
  });
});
