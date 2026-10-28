/// <reference types="bun" />
import { describe, expect, test } from "bun:test";

import { buildCsp, createNonce, cspSurface } from "./csp";

function directive(policy: string, name: string): string[] {
  const entry = policy.split("; ").find((part) => part.startsWith(`${name} `));
  return entry ? entry.split(" ").slice(1) : [];
}

describe("cspSurface", () => {
  test("classifies each surface", () => {
    expect(cspSurface("/admin")).toBe("admin");
    expect(cspSurface("/admin/customers/1")).toBe("admin");
    expect(cspSurface("/account/sign-in")).toBe("sign-in");
    expect(cspSurface("/account/sign-in/link")).toBe("sign-in");
    expect(cspSurface("/account/wallet")).toBe("customer");
    expect(cspSurface("/")).toBe("customer");
    expect(cspSurface("/pages/privacy")).toBe("customer");
    expect(cspSurface("/administrator")).toBe("customer");
  });
});

describe("buildCsp", () => {
  test("admits scripts only by nonce", () => {
    const policy = buildCsp({ nonce: "abc", surface: "customer" });
    const script = directive(policy, "script-src");
    expect(script).toContain("'nonce-abc'");
    expect(script).toContain("'strict-dynamic'");
    expect(script).not.toContain("'unsafe-inline'");
    expect(script).not.toContain("'unsafe-eval'");
    expect(directive(policy, "object-src")).toEqual(["'none'"]);
  });

  test("gives operator pages no third-party origin and no framing", () => {
    const policy = buildCsp({ nonce: "n", surface: "admin" });
    expect(policy).not.toContain("yandex");
    expect(policy).not.toContain("google");
    expect(policy).not.toContain("telegram");
    expect(directive(policy, "frame-ancestors")).toEqual(["'none'"]);
    expect(directive(policy, "frame-src")).toEqual(["'none'"]);
  });

  test("allows the counters on customer pages only", () => {
    const policy = buildCsp({ nonce: "n", surface: "customer" });
    expect(directive(policy, "connect-src")).toContain("https://mc.yandex.ru");
    expect(directive(policy, "connect-src")).toContain("https://*.google-analytics.com");
    expect(directive(policy, "frame-ancestors")).toContain("https://web.telegram.org");
    expect(policy).not.toContain("oauth.telegram.org");
  });

  test("allows the Telegram widget, and eval, on the sign-in screen only", () => {
    const policy = buildCsp({ nonce: "n", surface: "sign-in" });
    expect(directive(policy, "frame-src")).toContain("https://oauth.telegram.org");
    expect(directive(policy, "script-src")).toContain("'unsafe-eval'");
  });

  test("allows eval in development for React's overlay", () => {
    const policy = buildCsp({ dev: true, nonce: "n", surface: "admin" });
    expect(directive(policy, "script-src")).toContain("'unsafe-eval'");
  });
});

describe("createNonce", () => {
  test("is fresh and base64", () => {
    const first = createNonce();
    expect(first).toMatch(/^[A-Za-z0-9+/]+=*$/);
    expect(first.length).toBeGreaterThanOrEqual(22);
    expect(createNonce()).not.toBe(first);
  });
});
