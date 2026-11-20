import { describe, expect, test } from "bun:test";

import { isMiniAppFragment } from "./telegram-miniapp";

describe("isMiniAppFragment", () => {
  test("recognises Telegram's launch parameters", () => {
    expect(isMiniAppFragment("#tgWebAppData=query_id%3DAA&tgWebAppVersion=8.0")).toBe(true);
    expect(isMiniAppFragment("#tgWebAppPlatform=ios")).toBe(true);
  });

  test("ignores an ordinary fragment", () => {
    expect(isMiniAppFragment("")).toBe(false);
    expect(isMiniAppFragment("#section-2")).toBe(false);
  });
});
