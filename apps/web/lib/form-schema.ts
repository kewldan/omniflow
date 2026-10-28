import * as z from "zod/mini";

import { currencyExponent, parseMajorToMinor } from "./money";

/**
 * Validation building blocks for the panels' forms.
 *
 * They are written against `zod/mini` rather than the full build because they
 * ship in the customer panel, which is held to a tight JavaScript budget and is
 * opened on a phone: the mini build is a fifth of the size for the same checks.
 * `zodResolver` accepts either.
 *
 * Inputs stay strings in the form — that is what a text field holds — and each
 * check says what is wrong with the string. Converting to the wire shape
 * happens once, at submission, with the same parsers the checks used.
 *
 * An issue's message is a key under `common.form.errors`, optionally followed
 * by `|` and JSON values for the message; `FieldMessage` renders it.
 */

/** Encodes a translatable issue message; see `FieldMessage`. */
export function issue(key: string, values?: Record<string, string | number>): string {
  return values ? `${key}|${JSON.stringify(values)}` : key;
}

/** Decodes what `issue` produced. */
export function decodeIssue(message: string): {
  key: string;
  values?: Record<string, string | number>;
} {
  const separator = message.indexOf("|");
  if (separator < 0) {
    return { key: message };
  }
  try {
    return {
      key: message.slice(0, separator),
      values: JSON.parse(message.slice(separator + 1)) as Record<string, string | number>,
    };
  } catch {
    return { key: message };
  }
}

/** A non-empty string after trimming. */
export function requiredText(max = 500) {
  return z.string().check(
    z.refine((value) => value.trim().length > 0, issue("required")),
    z.refine((value) => value.trim().length <= max, issue("maxLength", { max })),
  );
}

/** An ISO-style three-letter currency code, typed in either case. */
export function currencyCode() {
  return z
    .string()
    .check(z.refine((value) => /^[A-Za-z]{3}$/.test(value.trim()), issue("currency")));
}

/**
 * A whole number typed as text, within bounds. `optional` lets the field be
 * left empty, which several catalogue fields use to mean "unlimited".
 */
export function wholeNumber(bounds: { min?: number; max?: number; optional?: boolean } = {}) {
  return z.string().check(
    z.superRefine((value, context) => {
      const trimmed = value.trim();
      if (trimmed === "") {
        if (!bounds.optional) {
          context.issues.push({ code: "custom", input: value, message: issue("required") });
        }
        return;
      }
      if (!/^-?\d+$/.test(trimmed)) {
        context.issues.push({ code: "custom", input: value, message: issue("integer") });
        return;
      }
      const number = Number(trimmed);
      if (bounds.min !== undefined && number < bounds.min) {
        context.issues.push({
          code: "custom",
          input: value,
          message: issue("min", { min: bounds.min }),
        });
      } else if (bounds.max !== undefined && number > bounds.max) {
        context.issues.push({
          code: "custom",
          input: value,
          message: issue("max", { max: bounds.max }),
        });
      }
    }),
  );
}

/** A non-negative decimal (traffic in gigabytes, say), optionally empty. */
export function decimalNumber(bounds: { min?: number; optional?: boolean } = {}) {
  return z.string().check(
    z.superRefine((value, context) => {
      const trimmed = value.trim().replace(",", ".");
      if (trimmed === "") {
        if (!bounds.optional) {
          context.issues.push({ code: "custom", input: value, message: issue("required") });
        }
        return;
      }
      if (!/^\d+(\.\d+)?$/.test(trimmed)) {
        context.issues.push({ code: "custom", input: value, message: issue("number") });
        return;
      }
      if (bounds.min !== undefined && Number(trimmed) < bounds.min) {
        context.issues.push({
          code: "custom",
          input: value,
          message: issue("min", { min: bounds.min }),
        });
      }
    }),
  );
}

/** Reads a field `wholeNumber` accepted; empty is null. */
export function readWholeNumber(value: string): number | null {
  const trimmed = value.trim();
  return trimmed === "" ? null : Number(trimmed);
}

/** Reads a field `decimalNumber` accepted; empty is null. */
export function readDecimal(value: string): number | null {
  const trimmed = value.trim().replace(",", ".");
  return trimmed === "" ? null : Number(trimmed);
}

/**
 * The message for an amount typed in major units, or null when it is valid.
 *
 * `minimumMinor` is in minor units, the way the API states limits.
 */
export function moneyIssue(
  input: string,
  currency: string,
  options: {
    minimumMinor?: number;
    maximumMinor?: number;
    formatted?: (minor: number) => string;
  } = {},
): string | null {
  const parsed = parseMajorToMinor(input, currency);
  if (!parsed.ok) {
    return parsed.reason === "precision"
      ? issue("money.precision", { decimals: currencyExponent(currency) })
      : issue(`money.${parsed.reason}`);
  }
  const show = options.formatted ?? String;
  if (options.minimumMinor !== undefined && parsed.minor < options.minimumMinor) {
    return issue("money.min", { min: show(options.minimumMinor) });
  }
  if (options.maximumMinor !== undefined && parsed.minor > options.maximumMinor) {
    return issue("money.max", { max: show(options.maximumMinor) });
  }
  return null;
}

/** One row of a per-currency price list, as the form holds it. */
export type PriceRow = { currency: string; amount: string };

/**
 * A list of prices, one per currency, each amount in that currency's major
 * unit. Every row is checked and every problem is reported on the row it
 * belongs to — the old free-text field silently dropped a row it could not
 * read, and saved what was left.
 */
export function priceList() {
  return z.array(z.object({ amount: z.string(), currency: z.string() })).check(
    z.superRefine((rows, context) => {
      if (rows.length === 0) {
        context.issues.push({ code: "custom", input: rows, message: issue("pricesRequired") });
        return;
      }
      const seen = new Set<string>();
      rows.forEach((row, index) => {
        const code = row.currency.trim().toUpperCase();
        if (!/^[A-Z]{3}$/.test(code)) {
          context.issues.push({
            code: "custom",
            input: row.currency,
            message: issue("currency"),
            path: [index, "currency"],
          });
        } else if (seen.has(code)) {
          context.issues.push({
            code: "custom",
            input: row.currency,
            message: issue("duplicateCurrency"),
            path: [index, "currency"],
          });
        }
        seen.add(code);
        const problem = /^[A-Z]{3}$/.test(code) ? moneyIssue(row.amount, code) : null;
        if (problem) {
          context.issues.push({
            code: "custom",
            input: row.amount,
            message: problem,
            path: [index, "amount"],
          });
        }
      });
    }),
  );
}

/** Converts a validated price list to the API's `{ CURRENCY: minor }` map. */
export function readPriceList(rows: readonly PriceRow[]): Record<string, number> {
  const prices: Record<string, number> = {};
  for (const row of rows) {
    const code = row.currency.trim().toUpperCase();
    const parsed = parseMajorToMinor(row.amount, code);
    if (parsed.ok) {
      prices[code] = parsed.minor;
    }
  }
  return prices;
}
