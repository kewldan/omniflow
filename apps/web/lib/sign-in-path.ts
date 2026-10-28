/**
 * Where a customer goes to sign in, and where they come back to afterwards.
 *
 * The panel sends a customer to the sign-in screen from two places: the shell,
 * when the session is gone, and the four screens that need a recent sign-in,
 * when the API answers `reauthentication_required`. In both cases the customer
 * was in the middle of something, and landing on the dashboard afterwards
 * throws that away. The path they were on travels as `?next=` and the sign-in
 * screen returns them to it.
 */

export const ACCOUNT_HOME = "/account";
export const SIGN_IN = "/account/sign-in";

/** The problem code the API answers when a session is too old for an action. */
export const REAUTHENTICATION_REQUIRED = "reauthentication_required";

export const ADMIN_HOME = "/admin";
/** Operator screens that are never somewhere to come back to after signing in. */
const ADMIN_AUTH_SCREENS = ["/admin/login", "/admin/setup"];

/** True when `value` is `screen` itself, a page beneath it, or it with a query. */
function isScreen(value: string, screen: string): boolean {
  return (
    value === screen ||
    value.startsWith(`${screen}/`) ||
    value.startsWith(`${screen}?`) ||
    value.startsWith(`${screen}#`)
  );
}

/**
 * Accepts only a same-site path inside one panel.
 *
 * Anything else — an absolute URL, a protocol-relative `//host`, a backslash
 * that a browser would read as one, a control character, a path outside the
 * panel, a sign-in screen itself — falls back to the panel's home, so the
 * parameter can never be used to bounce somebody to a site the panel did not
 * send them to.
 */
function safePathWithin(
  value: string | null | undefined,
  home: string,
  excluded: readonly string[],
): string {
  if (!value?.startsWith("/") || value.startsWith("//")) {
    return home;
  }
  // `/\evil.example` is `//evil.example` to a browser, and a tab or newline is
  // stripped from a URL before it is parsed, so either could smuggle a host.
  // biome-ignore lint/suspicious/noControlCharactersInRegex: rejecting control characters is the point
  if (/[\\\u0000-\u001f\u007f]/.test(value)) {
    return home;
  }
  if (!isScreen(value, home)) {
    return home;
  }
  if (excluded.some((screen) => isScreen(value, screen))) {
    return home;
  }
  return value;
}

/** Where a customer may be sent back to after signing in; see `safePathWithin`. */
export function safeNext(value: string | null | undefined): string {
  return safePathWithin(value, ACCOUNT_HOME, [SIGN_IN]);
}

/** Where an operator may be sent back to after signing in; see `safePathWithin`. */
export function safeAdminNext(value: string | null | undefined): string {
  return safePathWithin(value, ADMIN_HOME, ADMIN_AUTH_SCREENS);
}

/** The sign-in screen, remembering `next` when it is worth remembering. */
export function signInPath(next?: string | null): string {
  const target = safeNext(next);
  if (target === ACCOUNT_HOME) {
    return SIGN_IN;
  }
  return `${SIGN_IN}?next=${encodeURIComponent(target)}`;
}
