import { apiFetch } from "@/lib/api";

/**
 * Carrying a referral code from an invite link to the sign-in that follows.
 *
 * The panel builds `<public URL>/account/sign-in?ref=<code>` for a customer to
 * share. Until now nothing read that parameter back: a friend who followed the
 * link signed in normally and the inviter was never credited, while the same
 * friend pressing `/start ref_<code>` in the bot was. The code is held in
 * session storage across the sign-in round trip — a widget sign-in stays on
 * the page, an OIDC sign-in leaves and comes back — and posted once a session
 * exists, which is the moment the bot's `/start` attributes.
 *
 * Session storage rather than local storage: an invite is spent by the sign-in
 * it led to, and a code that survived into next month's unrelated visit would
 * credit an invitation nobody followed.
 */

const STORAGE_KEY = "omniflow.referral";

/** The schema's own shape for a code, so nothing unshaped is ever stored or sent. */
const CODE = /^[A-Za-z0-9]{10}$/;

/** Holds a code found on the sign-in screen. An invalid or absent value is ignored. */
export function rememberReferralCode(code: string | null | undefined): void {
  if (typeof window === "undefined" || !code || !CODE.test(code)) {
    return;
  }
  try {
    // First write wins for the session: the link that brought the visitor
    // here is the one that counts, not one they opened afterwards.
    if (!window.sessionStorage.getItem(STORAGE_KEY)) {
      window.sessionStorage.setItem(STORAGE_KEY, code.toUpperCase());
    }
  } catch {
    // Storage can be unavailable in a private window; an unattributed sign-up
    // is the worst outcome, and it is the one that happened before too.
  }
}

function takeReferralCode(): string | null {
  if (typeof window === "undefined") {
    return null;
  }
  try {
    const code = window.sessionStorage.getItem(STORAGE_KEY);
    window.sessionStorage.removeItem(STORAGE_KEY);
    return code && CODE.test(code) ? code : null;
  } catch {
    return null;
  }
}

/**
 * Posts a held code against the signed-in customer, then forgets it.
 *
 * Called once the shell has a session. It never throws: the customer has just
 * signed in, and an attribution that did not land is a missing reward rather
 * than a failed sign-in. The server decides whether the code counts — the
 * programme may be off, the code unknown, the customer not new — and the
 * answer is not surfaced, because none of it is something the invited person
 * can act on.
 */
export async function flushReferralAttribution(): Promise<void> {
  const code = takeReferralCode();
  if (!code) {
    return;
  }
  try {
    await apiFetch("/v1/account/referrals/attribution", {
      body: JSON.stringify({ code }),
      method: "POST",
    });
  } catch {
    // Deliberately silent; see above.
  }
}
