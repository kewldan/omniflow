"use client";

import { Button } from "@omniflow/ui/button";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { useEffect, useState } from "react";

import { ApiError, apiFetch } from "@/lib/api";
import { SIGN_IN, safeNext } from "@/lib/sign-in-path";

/** Refusals the sign-in screen has a sentence for; anything else is `generic`. */
const KNOWN_REFUSALS = ["link_invalid", "method_unavailable", "account_unavailable"];

/**
 * Where a link the bot delivered lands, after the API has checked it.
 *
 * The API's GET for the link only validates the token and redirects here with
 * the token in the fragment: a GET can be triggered by any page, so opening a
 * session on it would let a link requested for one account be planted in
 * somebody else's browser. The session is opened by the POST this page sends
 * when the customer presses "Continue" — their own click, on this origin.
 *
 * The token is read from the fragment, which the browser never sends to a
 * server or puts in a Referer, and is cleared from the address bar at once so
 * it does not stay in history or get copied along with the URL.
 */
export default function ConfirmMagicLinkPage() {
  const translate = useTranslations("account.signIn");
  const router = useRouter();
  const search = useSearchParams();
  const next = safeNext(search.get("next"));
  // undefined while the fragment has not been read yet; null when it had no token.
  const [token, setToken] = useState<string | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const fragment = new URLSearchParams(window.location.hash.replace(/^#/, ""));
    setToken(fragment.get("token") || null);
    if (window.location.hash) {
      window.history.replaceState(
        window.history.state,
        "",
        window.location.pathname + window.location.search,
      );
    }
  }, []);

  async function confirm() {
    if (!token) {
      return;
    }
    setBusy(true);
    try {
      await apiFetch("/v1/account/auth/link", {
        body: JSON.stringify({ token }),
        method: "POST",
      });
      router.replace(next);
    } catch (failure) {
      const code = failure instanceof ApiError ? (failure.code ?? "") : "";
      const reason = KNOWN_REFUSALS.includes(code) ? code : "generic";
      router.replace(`${SIGN_IN}?error=${reason}`);
    }
  }

  return (
    <main className="mx-auto flex min-h-dvh max-w-sm flex-col justify-center gap-6 px-6 py-16">
      <header className="space-y-2">
        <h1 className="font-bold text-[26px] tracking-[-0.035em]">{translate("confirm.title")}</h1>
        <p className="text-[14px] text-muted-foreground leading-relaxed">
          {token === null ? translate("errors.link_invalid") : translate("confirm.description")}
        </p>
      </header>

      {token === null ? (
        <Button asChild size="lg" variant="outline">
          <Link href={SIGN_IN}>{translate("confirm.back")}</Link>
        </Button>
      ) : (
        <Button
          aria-busy={busy}
          className="w-full"
          disabled={!token || busy}
          onClick={confirm}
          size="lg"
        >
          {busy ? translate("confirm.continuing") : translate("confirm.continue")}
        </Button>
      )}
    </main>
  );
}
