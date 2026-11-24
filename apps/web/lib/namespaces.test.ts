/// <reference types="bun" />
import { describe, expect, test } from "bun:test";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

/**
 * Each panel hands the browser only its own catalogue namespace (see
 * components/intl/scoped-intl-provider.tsx), so an operator screen that reads
 * `account.*` copy renders the raw key, and the other way round. This was how
 * the smart subscription's built-in rules showed `account.smart.builtin.…` on
 * the operator panel.
 */
const ROOT = join(import.meta.dir, "..");

function files(directory: string): string[] {
  return readdirSync(directory).flatMap((name) => {
    const path = join(directory, name);
    if (statSync(path).isDirectory()) {
      return files(path);
    }
    return /\.tsx?$/.test(name) && !name.endsWith(".test.ts") ? [path] : [];
  });
}

function offenders(directories: string[], foreign: string): string[] {
  const pattern = new RegExp(`useTranslations\\(\\s*["']${foreign}[."']`);
  return directories
    .flatMap((directory) => files(join(ROOT, directory)))
    .filter((path) => pattern.test(readFileSync(path, "utf8")))
    .map((path) => path.slice(ROOT.length + 1));
}

describe("catalogue namespaces", () => {
  test("operator screens read no customer panel copy", () => {
    expect(offenders(["app/(admin)", "components/admin"], "account")).toEqual([]);
  });

  test("customer screens read no operator panel copy", () => {
    expect(offenders(["app/(customer)", "components/account"], "admin")).toEqual([]);
  });
});
