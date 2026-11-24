/// <reference types="bun" />
import { describe, expect, test } from "bun:test";
import { existsSync } from "node:fs";
import { join } from "node:path";

import { ACCOUNT_ANIMATION_KEYS } from "./account-navigation";
import { NAVIGATION_ITEMS } from "./navigation";

/**
 * Every sidebar entry is animated, in both panels: a new entry ships with its
 * Lottie file from design/sidebar-icons-lottie/lottie_gen.py, packed by
 * tools/pack-lottie.py, or this fails.
 */
const LOTTIE = join(import.meta.dir, "..", "public", "lottie");

describe("sidebar animations", () => {
  test("every operator sidebar entry has one", () => {
    const missing = NAVIGATION_ITEMS.map((item) => item.key).filter(
      (key) => !existsSync(join(LOTTIE, "sidebar", `${key}.lottie`)),
    );
    expect(missing).toEqual([]);
  });

  test("every customer panel entry has one", () => {
    const missing = ACCOUNT_ANIMATION_KEYS.filter(
      (key) => !existsSync(join(LOTTIE, "account", `${key}.lottie`)),
    );
    expect(missing).toEqual([]);
  });
});
