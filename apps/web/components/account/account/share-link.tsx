"use client";

import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import { toast } from "@omniflow/ui/toast";
import { Check, Copy, Share2 } from "lucide-react";
import { useTranslations } from "next-intl";
import { useState } from "react";

import type { ReferralLinkReason } from "@/components/account/account/types";

/**
 * The invite code, and the link built from it when there is one.
 *
 * The code is the headline rather than the link because the code is the thing
 * that always exists: an installation with no public URL configured still mints
 * one, and a friend can type it in even when nothing can be shared from here.
 * The link and its controls appear underneath only when the server said a link
 * could be built.
 */
export function ShareLink({
  code,
  link,
  linkAvailable,
  linkReason,
  shareText,
  shareTitle,
}: {
  code: string;
  link: string;
  linkAvailable: boolean;
  linkReason?: ReferralLinkReason;
  shareText: string;
  shareTitle: string;
}) {
  const translate = useTranslations("account.account");
  const [shared, setShared] = useState<"copied" | "sent" | null>(null);

  function confirm(outcome: "copied" | "sent") {
    setShared(outcome);
    toast.success(translate(outcome === "sent" ? "referrals.shared" : "referrals.copied"));
    // The button reverts to its resting label so a second share is obviously
    // possible; the live region below has already announced the outcome.
    setTimeout(() => setShared(null), 1800);
  }

  /**
   * Share where the platform has a share sheet, copy where it does not.
   *
   * The share sheet is preferred because a referral link is sent to a person,
   * and on a phone that is one tap into the messenger the friend already uses
   * rather than a copy the customer then has to paste somewhere. A dismissed
   * sheet is not a failure — the customer changed their mind — so an AbortError
   * leaves the screen exactly as it was rather than reporting an error.
   */
  async function share() {
    if (typeof navigator !== "undefined" && typeof navigator.share === "function") {
      try {
        await navigator.share({ text: shareText, title: shareTitle, url: link });
        confirm("sent");
        return;
      } catch (shareError) {
        if ((shareError as DOMException)?.name === "AbortError") {
          return;
        }
        // Anything else — a browser that advertises the API but refuses it in
        // this context — falls through to the clipboard rather than dead-ending.
      }
    }
    await copy();
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(link);
      confirm("copied");
    } catch {
      toast.error(translate("referrals.copyFailed"));
    }
  }

  return (
    <section className="space-y-3 rounded-xl border border-border bg-card p-4">
      <div>
        <h2 className="font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
          {translate("referrals.code")}
        </h2>
        <p
          className="mt-1.5 select-all font-bold font-mono text-[24px] tracking-[0.08em]"
          data-numeric
        >
          {code}
        </p>
      </div>

      {linkAvailable ? (
        <>
          <p
            className={cn(
              "truncate rounded-md bg-background px-3 py-2.5 font-mono text-[11px]",
              "text-subtle-foreground",
            )}
          >
            {link}
          </p>
          <div className="flex gap-2">
            <Button className="flex-1" onClick={share} size="lg">
              {shared === "sent" ? <Check aria-hidden /> : <Share2 aria-hidden />}
              {translate("referrals.share")}
            </Button>
            <Button
              aria-label={translate("referrals.copy")}
              onClick={copy}
              size="lg"
              variant="outline"
            >
              {shared === "copied" ? <Check aria-hidden /> : <Copy aria-hidden />}
            </Button>
          </div>
        </>
      ) : (
        // No link, and the reason said out loud. Rendering a disabled share
        // button with no explanation would read as a bug in the panel rather
        // than as a setting nobody has filled in yet.
        <p className="rounded-md border border-border border-dashed px-3 py-2.5 text-[12.5px] text-muted-foreground leading-relaxed">
          {translate(`referrals.linkReason.${linkReason ?? "public_url_not_configured"}`)}
        </p>
      )}

      {/* The confirmation a screen reader hears. The icon swap is the sighted
          equivalent, and neither is the only signal. */}
      <p aria-live="polite" className="sr-only" role="status">
        {shared === "sent" && translate("referrals.shared")}
        {shared === "copied" && translate("referrals.copied")}
      </p>
    </section>
  );
}
