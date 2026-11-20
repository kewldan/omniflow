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

/** What a mid-period capacity change costs or hands back, as the server priced it. */
export type CapacityQuote = {
  currency: string;
  chargeMinor: number;
  creditMinor: number;
  creditAppliedMinor: number;
  creditToWalletMinor: number;
  walletMinor: number;
  externalMinor: number;
  walletBalanceMinor: number;
};

type Option = {
  addonVersionId?: string;
  name: string;
  trafficBytes: number;
  direction: "up" | "down";
  quote: CapacityQuote;
};

type Packages = {
  available: boolean;
  currency: string;
  currentTrafficBytes: number;
  options: Option[];
};

/**
 * The preview of one capacity change: every figure is a field the server
 * computed, never one derived here.
 */
export function CapacityQuoteLines({ quote }: { quote: CapacityQuote }) {
  const translate = useTranslations("account.capacity");
  const money = useMoney();
  const amount = (minor: number) => money(minor, quote.currency);
  return (
    <dl className="space-y-1 text-[12.5px]">
      {quote.chargeMinor !== 0 && (
        <Line label={translate("charge")} value={amount(quote.chargeMinor)} />
      )}
      {quote.creditMinor !== 0 && (
        <Line label={translate("credit")} value={`−${amount(quote.creditMinor)}`} />
      )}
      {quote.walletMinor !== 0 && (
        <Line label={translate("wallet")} value={`−${amount(quote.walletMinor)}`} />
      )}
      <Line label={translate("external")} value={amount(quote.externalMinor)} />
      {quote.creditToWalletMinor !== 0 && (
        <Line label={translate("toWallet")} value={`+${amount(quote.creditToWalletMinor)}`} />
      )}
    </dl>
  );
}

/**
 * The preview of a chosen change, with the one button that commits it. It is
 * shown in place rather than in a dialog so the figures stay readable beside
 * the option they belong to.
 */
export function ChangePreview({
  busy,
  onCancel,
  onConfirm,
  quote,
  summary,
}: {
  busy: boolean;
  onCancel: () => void;
  onConfirm: () => void;
  quote: CapacityQuote;
  summary: string;
}) {
  const translate = useTranslations("account.capacity");
  return (
    <div aria-live="polite" className="space-y-3 rounded-md border border-border bg-muted/40 p-3">
      <p className="text-[12.5px]">{summary}</p>
      <CapacityQuoteLines quote={quote} />
      <div className="flex gap-2">
        <Button disabled={busy} onClick={onConfirm} size="sm">
          {translate("confirm")}
        </Button>
        <Button disabled={busy} onClick={onCancel} size="sm" variant="outline">
          {translate("cancel")}
        </Button>
      </div>
    </div>
  );
}

function Line({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="font-medium" data-numeric>
        {value}
      </dd>
    </div>
  );
}

/**
 * Switching the subscription's traffic package to a bigger or a smaller one
 * part-way through the period. A bigger one costs its prorated price less
 * what is left of the current packages; a smaller one hands the difference
 * back to the wallet.
 */
export function TrafficPackage({
  onChanged,
  subscriptionId,
}: {
  onChanged: () => void;
  subscriptionId: string;
}) {
  const translate = useTranslations("account.capacity");
  const bytes = useBytes();
  const router = useRouter();
  const problem = useProblemMessage();
  const submission = useSubmission();
  const [chosen, setChosen] = useState<Option | null>(null);
  const [busy, setBusy] = useState(false);
  const key = `/v1/account/subscriptions/${subscriptionId}/traffic-package`;
  const { data, mutate } = useSWR<Packages, ApiError>(key, fetcher);

  if (!data?.available) {
    return null;
  }

  async function change(option: Option) {
    setBusy(true);
    const idempotencyKey = submission.begin();
    try {
      const order = await apiFetch<{ id: string; externalMinor: number }>(key, {
        body: JSON.stringify(
          option.addonVersionId ? { addonVersionId: option.addonVersionId } : {},
        ),
        headers: { "Idempotency-Key": idempotencyKey },
        method: "POST",
      });
      submission.settle();
      if (order.externalMinor > 0) {
        router.push(`/account/orders/${order.id}`);
        return;
      }
      toast.success(translate("done"));
      void mutate();
      onChanged();
    } catch (failure) {
      submission.settle(failure);
      toast.error(problem(failure));
    } finally {
      setBusy(false);
      setChosen(null);
    }
  }

  const label = (option: Option) =>
    option.addonVersionId ? option.name : translate("trafficNone");

  return (
    <section className="space-y-3 rounded-lg border border-border bg-card p-4">
      <SectionLabel>{translate("trafficTitle")}</SectionLabel>
      <p className="text-[12px] text-muted-foreground">
        {data.currentTrafficBytes > 0
          ? translate("trafficCurrent", { amount: bytes(data.currentTrafficBytes) })
          : translate("trafficCurrentNone")}
      </p>
      <ul className="space-y-2">
        {data.options.map((option) => (
          <li
            className="flex items-center justify-between gap-3"
            key={option.addonVersionId ?? "none"}
          >
            <span className="text-[13px]">
              {label(option)}
              {option.trafficBytes > 0 && (
                <span className="text-muted-foreground"> · {bytes(option.trafficBytes)}</span>
              )}
            </span>
            <Button disabled={busy} onClick={() => setChosen(option)} size="sm" variant="outline">
              {translate(option.direction === "up" ? "up" : "down")}
            </Button>
          </li>
        ))}
      </ul>
      {chosen && (
        <ChangePreview
          busy={busy}
          onCancel={() => setChosen(null)}
          onConfirm={() => change(chosen)}
          quote={chosen.quote}
          summary={translate("trafficConfirm", { name: label(chosen) })}
        />
      )}
    </section>
  );
}
