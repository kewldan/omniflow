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

/**
 * Accepts only a same-site path inside the panel.
 *
 * Anything else — an absolute URL, a protocol-relative `//host`, a path outside
 * `/account`, the sign-in screen itself — falls back to the dashboard, so the
 * parameter can never be used to bounce a customer somewhere the panel did not
 * send them.
 */
export function safeNext(value: string | null | undefined): string {
  if (!value?.startsWith("/account") || value.startsWith("//")) {
    return ACCOUNT_HOME;
  }
  if (value === SIGN_IN || value.startsWith(`${SIGN_IN}/`) || value.startsWith(`${SIGN_IN}?`)) {
    return ACCOUNT_HOME;
  }
  if (value !== ACCOUNT_HOME && !value.startsWith(`${ACCOUNT_HOME}/`)) {
    return ACCOUNT_HOME;
  }
  return value;
}

/** The sign-in screen, remembering `next` when it is worth remembering. */
export function signInPath(next?: string | null): string {
  const target = safeNext(next);
  if (target === ACCOUNT_HOME) {
    return SIGN_IN;
  }
  return `${SIGN_IN}?next=${encodeURIComponent(target)}`;
}
