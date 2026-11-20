"use client";

import { Button } from "@omniflow/ui/button";
import { toast } from "@omniflow/ui/toast";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { useState } from "react";

import { apiFetch } from "@/lib/api";
import { useBytes, useMoney } from "@/lib/format";

import { useProblemMessage } from "./reasons";
import type { Money, PlanOffer } from "./types";

/** `GET /v1/account/simple-subscription`. */
export type SimpleSubscription = {
  enabled: boolean;
  planVersionId?: string;
  plan?: PlanOffer;
  periodDays?: number;
  trafficGb?: number;
  extraDevices?: number;
  total?: Money;
};

/**
 * The simple subscription: one plan and one Buy button, its period, traffic,
 * and extra devices chosen by the operator in advance. The button opens the
 * checkout already configured, so all that is left is the payment method.
 */
export function SimpleOffer({ offer }: { offer: SimpleSubscription }) {
  const translate = useTranslations("account.simpleSubscription");
  const money = useMoney();
  const bytes = useBytes();
  const router = useRouter();
  const problem = useProblemMessage();
  const [busy, setBusy] = useState(false);
  const plan = offer.plan;
  if (!plan || !offer.total) {
    return null;
  }

  async function buy() {
    setBusy(true);
    try {
      await apiFetch("/v1/account/simple-subscription/checkout", { method: "POST" });
      router.push("/account/checkout");
    } catch (failure) {
      toast.error(problem(failure));
      setBusy(false);
    }
  }

  const traffic = offer.trafficGb
    ? translate("gb", { gb: offer.trafficGb })
    : plan.trafficAllowanceBytes === null
      ? translate("unlimited")
      : bytes(plan.trafficAllowanceBytes);
  const days = offer.periodDays || Math.round(plan.durationSeconds / 86_400);

  return (
    <section className="space-y-3 rounded-lg border border-primary/40 bg-card p-4">
      <div>
        <h2 className="font-semibold text-[17px]">{plan.name}</h2>
        {plan.description && (
          <p className="mt-1 text-[12.5px] text-muted-foreground leading-relaxed">
            {plan.description}
          </p>
        )}
      </div>
      <ul className="space-y-1 text-[13px]">
        <li>{translate("period", { days })}</li>
        <li>{translate("traffic", { traffic })}</li>
        {plan.deviceLimit !== null && (
          <li>{translate("devices", { count: plan.deviceLimit + (offer.extraDevices ?? 0) })}</li>
        )}
      </ul>
      <Button className="w-full" disabled={busy} onClick={buy} size="lg">
        {translate("buy", { price: money(offer.total.amountMinor, offer.total.currency) })}
      </Button>
    </section>
  );
}
