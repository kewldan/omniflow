/// <reference types="bun" />
import { describe, expect, test } from "bun:test";
import { createTranslator } from "next-intl";

import en from "../messages/en.json";
import fa from "../messages/fa.json";
import ru from "../messages/ru.json";
import uk from "../messages/uk.json";
import zh from "../messages/zh.json";

/**
 * The two catalogues are one contract in two languages.
 *
 * A key present in one file only renders as a missing-message marker in the
 * other language; a placeholder renamed in one file renders the raw `{name}`
 * or throws; and a message ICU cannot parse — a literal `<b>` read as a rich
 * text tag was the one that shipped — fails on the screen that shows it. None
 * of that is visible to the type checker, so it is checked here.
 */

type Catalogue = { [key: string]: string | Catalogue };

function flatten(catalogue: Catalogue, prefix = "", out = new Map<string, string>()) {
  for (const [key, value] of Object.entries(catalogue)) {
    const path = prefix ? `${prefix}.${key}` : key;
    if (typeof value === "string") {
      out.set(path, value);
    } else {
      flatten(value, path, out);
    }
  }
  return out;
}

/**
 * The argument names a message takes: `{name}`, `{count, plural, …}`,
 * `{date, date, medium}`. Plural and select branch bodies (`one {…}`) are
 * stripped first so a one-word branch is not mistaken for an argument.
 */
function argumentsOf(message: string): string[] {
  const withoutQuoted = message.replace(/'[^']*'/g, "");
  const withoutBranches = withoutQuoted.replace(
    /(?:\b(?:zero|one|two|few|many|other)|=\d+)\s*\{/g,
    "⟨",
  );
  const names = [...withoutBranches.matchAll(/\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*[,}]/g)].map(
    (match) => match[1] as string,
  );
  return [...new Set(names)].sort();
}

const english = flatten(en as Catalogue);
const russian = flatten(ru as Catalogue);

describe("message catalogues", () => {
  test("Russian has every English key", () => {
    expect([...english.keys()].filter((key) => !russian.has(key))).toEqual([]);
  });

  test("English has every Russian key", () => {
    expect([...russian.keys()].filter((key) => !english.has(key))).toEqual([]);
  });

  test("no message is empty", () => {
    const empty = [...english, ...russian]
      .filter(([, value]) => value.trim() === "")
      .map(([key]) => key);
    expect(empty).toEqual([]);
  });

  test("both languages take the same arguments", () => {
    const mismatched = [...english]
      .filter(([key]) => russian.has(key))
      .filter(
        ([key, value]) =>
          argumentsOf(value).join() !== argumentsOf(russian.get(key) as string).join(),
      )
      .map(
        ([key, value]) =>
          `${key}: en(${argumentsOf(value)}) ru(${argumentsOf(russian.get(key) as string)})`,
      );
    expect(mismatched).toEqual([]);
  });

  for (const [locale, catalogue, flat] of [
    ["en", en, english],
    ["ru", ru, russian],
  ] as const) {
    test(`every ${locale} message parses as ICU`, () => {
      const invalid: string[] = [];
      const translate = createTranslator({
        locale,
        messages: catalogue as Catalogue,
        onError: (error) => {
          // A missing argument is expected here — no values are passed. A
          // message that cannot be parsed at all is the defect.
          if (error.code === "INVALID_MESSAGE") {
            invalid.push(error.message.slice(0, 160));
          }
        },
        getMessageFallback: ({ key }) => key,
      });
      for (const key of flat.keys()) {
        // biome-ignore lint/suspicious/noExplicitAny: every key in the catalogue, checked dynamically. `rich` because plain `t()` skips parsing a message given no values, which is how a broken tag hid.
        (translate as any).rich(key, {});
      }
      expect(invalid).toEqual([]);
    });
  }
});

/**
 * The Ukrainian, Chinese, and Persian catalogues are partial: anything they do
 * not translate renders from English. What they do translate must be a key
 * English has, with the same arguments, so the overlay never invents a message
 * or drops a placeholder.
 */
describe("partial catalogues", () => {
  for (const [locale, catalogue] of [
    ["uk", uk],
    ["zh", zh],
    ["fa", fa],
  ] as const) {
    test(`${locale} translates only English keys, with the same arguments`, () => {
      const partial = flatten(catalogue as Catalogue);
      const problems = [...partial]
        .filter(([key, message]) => {
          const source = english.get(key);
          return source === undefined || argumentsOf(source).join() !== argumentsOf(message).join();
        })
        .map(([key]) => key);
      expect(problems).toEqual([]);
    });
  }
});
