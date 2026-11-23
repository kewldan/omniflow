"use client";

import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { toast } from "@omniflow/ui/toast";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { useRef, useState } from "react";

import { ReauthNotice, useReauthentication } from "@/components/account/reauth";
import { SectionLabel } from "@/components/account/state";
import { useAccount } from "@/lib/account-session";
import { apiFetch } from "@/lib/api";

import { useProblemMessage } from "./reasons";

/**
 * Closing one of several subscriptions.
 *
 * The time it has left is given up and nothing is refunded, so the customer
 * types a phrase to confirm, and a stale sign-in is asked to sign in again
 * first. The first subscription is never offered this: the server refuses it.
 */
export function CloseSubscription({ subscriptionId }: { subscriptionId: string }) {
  const translate = useTranslations("account.closeSubscription");
  const translateAccount = useTranslations("account");
  const router = useRouter();
  const problem = useProblemMessage();
  const { session } = useAccount();
  const { redirectIfRequired } = useReauthentication();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);
  const needsReauth = session?.session.reauthenticationRequired ?? false;

  async function close() {
    if (inFlight.current) {
      return;
    }
    inFlight.current = true;
    setBusy(true);
    try {
      await apiFetch(`/v1/account/subscriptions/${subscriptionId}`, { method: "DELETE" });
      toast.success(translate("closed"));
      router.push("/account");
    } catch (failure) {
      if (!redirectIfRequired(failure)) {
        toast.error(problem(failure));
      }
    } finally {
      inFlight.current = false;
      setBusy(false);
      setOpen(false);
    }
  }

  return (
    <section className="space-y-3">
      <SectionLabel>{translate("title")}</SectionLabel>
      <p className="text-[13px] text-muted-foreground leading-relaxed">{translate("warning")}</p>
      {needsReauth && <ReauthNotice />}
      <Button
        className="w-full"
        disabled={busy}
        onClick={() => setOpen(true)}
        size="lg"
        variant="destructive-outline"
      >
        {translate("action")}
      </Button>
      <ConfirmDialog
        cancelLabel={translateAccount("actions.cancel")}
        confirmationPhrase={translate("phrase")}
        confirmationPrompt={translate("prompt")}
        confirmLabel={translate("action")}
        description={translate("warning")}
        destructive
        onConfirm={close}
        onOpenChange={setOpen}
        open={open}
        pending={busy}
        title={translate("action")}
      />
    </section>
  );
}
