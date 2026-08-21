"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { useCallback } from "react";

import type { ApiError } from "@/lib/api";
import { REAUTHENTICATION_REQUIRED, signInPath } from "@/lib/sign-in-path";

/**
 * Re-authentication, handled the same way on every screen that needs it.
 *
 * Four actions are refused once a session is older than fifteen minutes:
 * unlinking a sign-in method, rotating the access link, disconnecting every
 * device, and requesting deletion. The remedy is a fresh sign-in, and the
 * customer was in the middle of something — so the sign-in screen is given the
 * path to come back to rather than dropping them on the dashboard.
 */
export function useReauthentication() {
  const router = useRouter();
  const pathname = usePathname();
  const href = signInPath(pathname);

  /**
   * Sends the customer to sign in again when the problem is a stale session.
   * Reports whether it did, so the caller can fall through to its own error
   * handling for everything else.
   */
  const redirectIfRequired = useCallback(
    (problem: unknown): boolean => {
      if ((problem as ApiError | undefined)?.code !== REAUTHENTICATION_REQUIRED) {
        return false;
      }
      router.push(href);
      return true;
    },
    [href, router],
  );

  return { href, redirectIfRequired };
}

/**
 * The notice shown before a gated control when the session is already known to
 * be too old, so the customer is not walked through a confirmation only to be
 * refused at the end of it.
 */
export function ReauthNotice() {
  const translate = useTranslations("account");
  const { href } = useReauthentication();
  return (
    <p
      className="rounded-lg border border-warning/40 bg-warning/10 px-3 py-2.5 text-[12.5px] leading-relaxed"
      role="status"
    >
      {translate("states.reauthenticate")}{" "}
      <Link className="font-medium underline underline-offset-2" href={href}>
        {translate("states.signInAgain")}
      </Link>
    </p>
  );
}
