"use client";

import { Button } from "@omniflow/ui/button";
import { toast } from "@omniflow/ui/toast";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { SectionLabel } from "@/components/account/state";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useBytes, useMoney } from "@/lib/format";
import { useSubmission } from "@/lib/idempotency";

import { useProblemMessage } from "./reasons";

type Money = { amountMinor: number; currency: string };

type SubscriptionAddon = {
  addonVersionId: string;
  kind: string;
  name: string;
  description: string;
  trafficBytes?: number | null;
  deviceSlots?: number | null;
  squadCount: number;
  price: Money;
  charge: Money;
};

type SubscriptionAddons = {
  available: boolean;
  reason?: string;
  currency: string;
  walletMinor: number;
  addons: SubscriptionAddon[];
};

/**
 * Extra traffic, device slots, and servers for a running subscription.
 *
 * The bot offered these alone; this is the same offer, priced by the same
 * proration, bought as the same order. The wallet settles an add-on it covers
 * at once; otherwise the order page takes the rest through a provider.
 */
export function SubscriptionAddons({
  onBought,
  subscriptionId,
}: {
  onBought: () => void;
  subscriptionId: string;
}) {
  const translate = useTranslations("account.subscriptionAddons");
  const money = useMoney();
  const bytes = useBytes();
  const router = useRouter();
  const problem = useProblemMessage();
  const submission = useSubmission();
  const [busy, setBusy] = useState("");
  const key = `/v1/account/subscriptions/${subscriptionId}/addons`;
  const { data, mutate } = useSWR<SubscriptionAddons, ApiError>(key, fetcher);

  if (!data?.available || data.addons.length === 0) {
    return null;
  }

  async function buy(addon: SubscriptionAddon) {
    setBusy(addon.addonVersionId);
    const idempotencyKey = submission.begin();
    try {
      const order = await apiFetch<{ id: string; externalMinor: number }>(key, {
        body: JSON.stringify({ addonVersionId: addon.addonVersionId, quantity: 1 }),
        headers: { "Idempotency-Key": idempotencyKey },
        method: "POST",
      });
      submission.settle();
      if (order.externalMinor > 0) {
        router.push(`/account/orders/${order.id}`);
        return;
      }
      toast.success(translate("bought", { name: addon.name }));
      await mutate();
      onBought();
    } catch (failure) {
      submission.settle(failure);
      toast.error(problem(failure));
    } finally {
      setBusy("");
    }
  }

  return (
    <section className="space-y-3 rounded-lg border border-border bg-card p-4">
      <SectionLabel>{translate("title")}</SectionLabel>
      <p className="text-[12px] text-muted-foreground">
        {translate("wallet", { amount: money(data.walletMinor, data.currency) })}
      </p>
      <ul className="space-y-2">
        {data.addons.map((addon) => (
          <li
            className="flex items-center justify-between gap-3 rounded-md border border-border p-3"
            key={addon.addonVersionId}
          >
            <div className="min-w-0">
              <p className="font-medium text-[14px]">{addon.name}</p>
              <p className="text-[12px] text-muted-foreground">
                {addon.trafficBytes
                  ? translate("traffic", { amount: bytes(addon.trafficBytes) })
                  : addon.deviceSlots
                    ? translate("devices", { count: addon.deviceSlots })
                    : translate("squads", { count: addon.squadCount })}
                {addon.charge.amountMinor < addon.price.amountMinor &&
                  ` · ${translate("prorated")}`}
              </p>
            </div>
            <Button disabled={busy !== ""} onClick={() => buy(addon)} size="sm" variant="outline">
              {busy === addon.addonVersionId
                ? translate("buying")
                : translate("buy", {
                    price: money(addon.charge.amountMinor, addon.charge.currency),
                  })}
            </Button>
          </li>
        ))}
      </ul>
    </section>
  );
}
