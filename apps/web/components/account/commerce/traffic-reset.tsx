"use client";

import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { toast } from "@omniflow/ui/toast";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { SectionLabel } from "@/components/account/state";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";
import { useSubmission } from "@/lib/idempotency";

import { useProblemMessage } from "./reasons";

type Quote = { available: boolean; price: { amountMinor: number; currency: string } };

/**
 * Paying to start the traffic counter from zero before the period ends.
 *
 * Shown only when the subscription's plan sells resets and its traffic is
 * limited. The wallet settles it when it covers the price; otherwise the order
 * page takes the rest.
 */
export function TrafficReset({
  onReset,
  subscriptionId,
}: {
  onReset: () => void;
  subscriptionId: string;
}) {
  const translate = useTranslations("account.trafficReset");
  const money = useMoney();
  const router = useRouter();
  const problem = useProblemMessage();
  const submission = useSubmission();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const key = `/v1/account/subscriptions/${subscriptionId}/traffic-reset`;
  const { data } = useSWR<Quote, ApiError>(key, fetcher);

  if (!data?.available) {
    return null;
  }
  const price = money(data.price.amountMinor, data.price.currency);

  async function buy() {
    setBusy(true);
    const idempotencyKey = submission.begin();
    try {
      const order = await apiFetch<{ id: string; externalMinor: number }>(key, {
        headers: { "Idempotency-Key": idempotencyKey },
        method: "POST",
      });
      submission.settle();
      if (order.externalMinor > 0) {
        router.push(`/account/orders/${order.id}`);
        return;
      }
      toast.success(translate("done"));
      onReset();
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
      <p className="text-[12px] text-muted-foreground">{translate("hint")}</p>
      <Button disabled={busy} onClick={() => setOpen(true)} size="sm" variant="outline">
        {translate("action", { price })}
      </Button>
      <ConfirmDialog
        cancelLabel={translate("cancel")}
        confirmLabel={translate("action", { price })}
        description={translate("confirm", { price })}
        onConfirm={buy}
        onOpenChange={setOpen}
        open={open}
        pending={busy}
        title={translate("title")}
      />
    </section>
  );
}
