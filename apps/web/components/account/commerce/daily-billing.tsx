"use client";

import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { toast } from "@omniflow/ui/toast";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { SectionLabel } from "@/components/account/state";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";
import { useSubmission } from "@/lib/idempotency";

import { useProblemMessage } from "./reasons";

type DailyBilling = {
  daily: boolean;
  paused?: boolean;
  suspended?: boolean;
  endsAt?: string;
  price?: { amountMinor: number; currency: string };
};

/**
 * How a subscription on a daily plan is billed, with the customer's pause.
 *
 * Shown only for a daily plan. A pause stops the daily charge and turns
 * access off; a resume gives back the part of the day the pause held. A day
 * the balance did not cover is not a pause: it resumes on its own after a
 * top-up, so the card links to the top-up instead.
 */
export function DailyBillingCard({
  onChanged,
  subscriptionId,
}: {
  onChanged: () => void;
  subscriptionId: string;
}) {
  const translate = useTranslations("account.dailyBilling");
  const money = useMoney();
  const problem = useProblemMessage();
  const submission = useSubmission();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const key = `/v1/account/subscriptions/${subscriptionId}/daily-billing`;
  const { data, mutate } = useSWR<DailyBilling, ApiError>(key, fetcher);

  if (!data?.daily || !data.price) {
    return null;
  }
  const price = money(data.price.amountMinor, data.price.currency);
  const pausing = !data.paused;

  async function apply() {
    setBusy(true);
    const idempotencyKey = submission.begin();
    try {
      const next = await apiFetch<DailyBilling>(key, {
        body: JSON.stringify({ paused: pausing }),
        headers: { "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
        method: "PUT",
      });
      submission.settle();
      await mutate(next, { revalidate: false });
      toast.success(translate(pausing ? "pausedDone" : "resumedDone"));
      onChanged();
    } catch (failure) {
      submission.settle(failure);
      toast.error(problem(failure));
    } finally {
      setBusy(false);
      setOpen(false);
    }
  }

  return (
    <section className="space-y-3 rounded-lg border border-border bg-card p-4">
      <SectionLabel>{translate("title")}</SectionLabel>
      <p className="text-[12px] text-muted-foreground">
        {data.paused
          ? translate("paused")
          : data.suspended
            ? translate("suspended", { price })
            : translate("running", { price })}
      </p>
      {data.suspended && !data.paused ? (
        <Button asChild size="sm" variant="outline">
          <Link href="/account/wallet">{translate("topUp")}</Link>
        </Button>
      ) : (
        <Button disabled={busy} onClick={() => setOpen(true)} size="sm" variant="outline">
          {translate(pausing ? "pause" : "resume")}
        </Button>
      )}
      <ConfirmDialog
        cancelLabel={translate("cancel")}
        confirmLabel={translate(pausing ? "pause" : "resume")}
        description={translate(pausing ? "pauseConfirm" : "resumeConfirm", { price })}
        onConfirm={apply}
        onOpenChange={setOpen}
        open={open}
        pending={busy}
        title={translate("title")}
      />
    </section>
  );
}
