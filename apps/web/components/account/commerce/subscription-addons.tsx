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

type SavedAddon = {
  addonVersionId: string;
  quantity: number;
  saved: Money;
  total: Money;
  missingMinor: number;
  stale: boolean;
  autoPurchase: boolean;
  expiresAt: string;
  lastFailure?: string;
};

type SubscriptionAddons = {
  available: boolean;
  reason?: string;
  currency: string;
  walletMinor: number;
  addons: SubscriptionAddon[];
  cart?: SavedAddon;
};

/**
 * Extra traffic, device slots, and servers for a running subscription.
 *
 * The bot offered these alone; this is the same offer, priced by the same
 * proration, bought as the same order. The wallet settles an add-on it covers
 * at once; otherwise the order page takes the rest through a provider, or the
 * add-on is saved and bought by itself once a top-up covers it.
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

  async function saveForLater(addon: SubscriptionAddon) {
    setBusy(addon.addonVersionId);
    try {
      const next = await apiFetch<SubscriptionAddons>(`${key}/cart`, {
        body: JSON.stringify({ addonVersionId: addon.addonVersionId, quantity: 1 }),
        method: "PUT",
      });
      await mutate(next, { revalidate: false });
      toast.success(translate("saved", { name: addon.name }));
      router.push("/account/wallet");
    } catch (failure) {
      toast.error(problem(failure));
    } finally {
      setBusy("");
    }
  }

  async function discard() {
    setBusy("cart");
    try {
      const next = await apiFetch<SubscriptionAddons>(`${key}/cart`, { method: "DELETE" });
      await mutate(next, { revalidate: false });
    } catch (failure) {
      toast.error(problem(failure));
    } finally {
      setBusy("");
    }
  }

  const saved = data.cart;
  const savedName = saved
    ? (data.addons.find((addon) => addon.addonVersionId === saved.addonVersionId)?.name ?? "")
    : "";

  return (
    <section className="space-y-3 rounded-lg border border-border bg-card p-4">
      <SectionLabel>{translate("title")}</SectionLabel>
      <p className="text-[12px] text-muted-foreground">
        {translate("wallet", { amount: money(data.walletMinor, data.currency) })}
      </p>
      {saved && (
        <div className="space-y-2 rounded-md border border-dashed bg-well border-border p-3">
          <p className="font-medium text-[13.5px]">
            {translate("savedTitle", { name: savedName || translate("savedUnnamed") })}
          </p>
          <p className="text-[12.5px] text-muted-foreground">
            {saved.stale
              ? translate("savedStale")
              : saved.lastFailure === "cart_price_changed"
                ? translate("savedPriceChanged")
                : saved.missingMinor > 0
                  ? translate("savedWaiting", {
                      missing: money(saved.missingMinor, saved.total.currency),
                      price: money(saved.total.amountMinor, saved.total.currency),
                    })
                  : translate("savedReady")}
          </p>
          <div className="flex flex-wrap gap-2">
            {!saved.stale && saved.missingMinor > 0 && (
              <Button onClick={() => router.push("/account/wallet")} size="sm" variant="outline">
                {translate("topUp")}
              </Button>
            )}
            <Button disabled={busy !== ""} onClick={discard} size="sm" variant="ghost">
              {translate("discard")}
            </Button>
          </div>
        </div>
      )}
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
            <div className="flex shrink-0 flex-col items-end gap-1">
              <Button disabled={busy !== ""} onClick={() => buy(addon)} size="sm" variant="outline">
                {busy === addon.addonVersionId
                  ? translate("buying")
                  : translate("buy", {
                      price: money(addon.charge.amountMinor, addon.charge.currency),
                    })}
              </Button>
              {addon.charge.amountMinor > data.walletMinor && (
                <Button
                  disabled={busy !== ""}
                  onClick={() => saveForLater(addon)}
                  size="sm"
                  variant="ghost"
                >
                  {translate("saveForLater")}
                </Button>
              )}
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}
