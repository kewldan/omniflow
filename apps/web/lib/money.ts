/**
 * Money, for both panels.
 *
 * Money crosses the wire as an integer count of minor units plus an ISO
 * currency code, which is the only representation that survives a round trip
 * without a rounding argument. Showing it to a person, or reading an amount a
 * person typed, needs the currency's exponent: two for EUR, zero for JPY, three
 * for KWD. Getting it wrong is not cosmetic — a KWD price read with two decimals
 * is shown ten times too large, and a VND one a hundred times too small.
 *
 * There is exactly one place that knows the exponent, and it is here. The two
 * panels used to have one each, and they disagreed for every currency outside a
 * hard-coded three.
 */

/**
 * Exponents Omniflow states rather than asks the runtime for.
 *
 * These mirror the API's own table (`internal/payments/amount.go`), which is
 * authoritative for what an integer amount means: the browser must divide by
 * the same power of ten the server multiplied by. Telegram Stars (`XTR`) are
 * whole units and not an ISO currency, so no runtime knows them at all.
 */
const EXPONENTS: Record<string, number> = {
  XTR: 0,
  JPY: 0,
  KRW: 0,
  BHD: 3,
  JOD: 3,
  KWD: 3,
  OMR: 3,
  TND: 3,
};

/** True for Telegram Stars, which have no symbol any runtime can render. */
export function isTelegramStars(currency: string): boolean {
  return currency.toUpperCase() === "XTR";
}

/**
 * How many decimal places a currency has.
 *
 * Anything outside the API's table is asked of `Intl`, which knows the ISO
 * exponent of every currency it can format; an unknown code falls back to two
 * rather than throwing.
 */
export function currencyExponent(currency: string): number {
  const code = currency.toUpperCase();
  const stated = EXPONENTS[code];
  if (stated !== undefined) {
    return stated;
  }
  try {
    return (
      new Intl.NumberFormat("en", { currency: code, style: "currency" }).resolvedOptions()
        .maximumFractionDigits ?? 2
    );
  } catch {
    return 2;
  }
}

/** Converts minor units to the major-unit number a formatter takes. */
export function toMajorUnits(minor: number, currency: string): number {
  return minor / 10 ** currencyExponent(currency);
}

/** Renders minor units as the plain decimal a person would type, e.g. "4.99". */
export function minorToInput(minor: number, currency: string): string {
  const exponent = currencyExponent(currency);
  const sign = minor < 0 ? "-" : "";
  const digits = Math.abs(Math.trunc(minor))
    .toString()
    .padStart(exponent + 1, "0");
  if (exponent === 0) {
    return `${sign}${digits}`;
  }
  return `${sign}${digits.slice(0, -exponent)}.${digits.slice(-exponent)}`;
}

export type ParsedAmount =
  | { ok: true; minor: number }
  | { ok: false; reason: "empty" | "invalid" | "precision" | "negative" | "tooLarge" };

/**
 * Reads an amount a person typed in major units ("4.99", "4,99", "1 200") into
 * integer minor units, without floating-point arithmetic.
 *
 * It refuses rather than rounds: more decimals than the currency has is a typo
 * or a misunderstanding, and silently rounding it would price something the
 * operator did not type.
 */
export function parseMajorToMinor(
  input: string,
  currency: string,
  options: { allowNegative?: boolean } = {},
): ParsedAmount {
  const compact = input.replace(/[\s  ]/g, "").replace(",", ".");
  if (compact === "") {
    return { ok: false, reason: "empty" };
  }
  const match = /^(-)?(\d+)(?:\.(\d*))?$/.exec(compact);
  if (!match) {
    return { ok: false, reason: "invalid" };
  }
  const [, minus, whole, fraction = ""] = match;
  if (minus && !options.allowNegative) {
    return { ok: false, reason: "negative" };
  }
  const exponent = currencyExponent(currency);
  if (fraction.length > exponent) {
    return { ok: false, reason: "precision" };
  }
  const minor = Number(`${whole}${fraction.padEnd(exponent, "0")}`);
  if (!Number.isSafeInteger(minor)) {
    return { ok: false, reason: "tooLarge" };
  }
  return { ok: true, minor: minus && minor !== 0 ? -minor : minor };
}

export type MoneyFormatOptions = {
  /**
   * For a chart axis, where the full figure would be wider than the plot it
   * labels. Never the right form for a number somebody has to act on, which is
   * why it has to be asked for.
   */
  compact?: boolean;
  /**
   * `narrowSymbol` ("$") for the customer panel, where one currency is in play
   * at a time; `symbol` ("US$") for the operator panel, where several can sit
   * in one table.
   */
  display?: "symbol" | "narrowSymbol";
};

/**
 * Formats an integer minor-unit amount in a locale.
 *
 * Telegram Stars render as "⭐ 1,200". A code the runtime has never heard of
 * renders as the number followed by the code rather than throwing: a price that
 * reads "1200 ABC" is still a price, while a thrown error is a blank screen.
 */
export function formatMoney(
  minor: number,
  currency: string,
  locale: string,
  options: MoneyFormatOptions = {},
): string {
  const code = currency.toUpperCase();
  const exponent = currencyExponent(code);
  const amount = minor / 10 ** exponent;
  const digits = options.compact
    ? { maximumFractionDigits: 1 }
    : { maximumFractionDigits: exponent, minimumFractionDigits: exponent };
  if (!isTelegramStars(code)) {
    try {
      return new Intl.NumberFormat(locale, {
        currency: code,
        currencyDisplay: options.display ?? "symbol",
        notation: options.compact ? "compact" : "standard",
        style: "currency",
        ...digits,
      }).format(amount);
    } catch {
      // Fall through to the plain form below.
    }
  }
  const number = new Intl.NumberFormat(locale, {
    notation: options.compact ? "compact" : "standard",
    ...digits,
  }).format(amount);
  // Stars are shown the way Telegram shows them, with the star before the
  // count; `Intl` would otherwise render the code "XTR" as if it were a
  // currency a customer could recognise.
  return isTelegramStars(code) ? `⭐ ${number}` : `${number} ${code}`;
}
