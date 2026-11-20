"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { toast } from "@omniflow/ui/toast";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import type { PlanCatalogue } from "@/components/account/commerce/types";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { Timestamp } from "@/components/intl/timestamp";
import { ApiError, apiFetch, fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";

type Gift = {
  id: string;
  orderId: string;
  code?: string;
  codeHint: string;
  kind: string;
  status: string;
  currency: string;
  orderState?: string;
  externalMinor: number;
  expiresAt: string;
  claimedAt?: string;
  createdAt: string;
  /** "@username" or "email" for an addressed gift. */
  recipient?: string;
  /** How far an addressed gift got: waiting, delivered, or closed. */
  delivery?: "waiting" | "delivered" | "closed";
};

/** Problem codes a recipient is refused with, each with its own message. */
const RECIPIENT_PROBLEMS = [
  "gift_recipient_invalid",
  "gift_recipient_self",
  "gift_email_unavailable",
] as const;

/**
 * Gifts on the web, as in the bot: buy a subscription for somebody, see the
 * code once, and track what was sent and received. A code someone gave you is
 * redeemed on the wallet page.
 */
export default function GiftsPage() {
  const translate = useTranslations("account.gifts");
  const { data, error, isLoading, mutate } = useSWR<{ sent: Gift[]; received: Gift[] }, ApiError>(
    "/v1/account/gifts",
    fetcher,
  );

  return (
    <div className="space-y-5">
      <BuyGift onBought={() => void mutate()} />

      <section className="space-y-3">
        <SectionLabel>{translate("sent")}</SectionLabel>
        {isLoading ? (
          <ListSkeleton rows={2} />
        ) : error ? (
          <AccountNotice title={translate("error")} variant="danger" />
        ) : (data?.sent ?? []).length === 0 ? (
          <p className="text-[13px] text-muted-foreground">{translate("emptySent")}</p>
        ) : (
          data?.sent.map((gift) => <GiftRow gift={gift} key={gift.id} />)
        )}
      </section>

      {(data?.received ?? []).length > 0 && (
        <section className="space-y-3">
          <SectionLabel>{translate("received")}</SectionLabel>
          {data?.received.map((gift) => (
            <GiftRow gift={gift} key={gift.id} />
          ))}
        </section>
      )}

      <p className="text-[12.5px] text-muted-foreground">
        {translate.rich("redeemHint", {
          link: (chunks) => (
            <Link className="underline" href="/account/wallet">
              {chunks}
            </Link>
          ),
        })}
      </p>
    </div>
  );
}

function GiftRow({ gift }: { gift: Gift }) {
  const translate = useTranslations("account.gifts");
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border bg-card p-3 text-[13px]">
      <span className="font-mono">…{gift.codeHint}</span>
      {gift.recipient && (
        <span className="text-muted-foreground">
          {translate("recipientFor", {
            recipient: gift.recipient === "email" ? translate("recipientEmail") : gift.recipient,
          })}
          {gift.delivery && ` · ${translate(`delivery.${gift.delivery}`)}`}
        </span>
      )}
      <Badge variant="outline">{translate(`status.${gift.status}`)}</Badge>
      <span className="text-muted-foreground">
        <Timestamp value={gift.claimedAt ?? gift.createdAt} />
      </span>
    </div>
  );
}

function BuyGift({ onBought }: { onBought: () => void }) {
  const translate = useTranslations("account.gifts");
  const money = useMoney();
  const planId = useId();
  const messageId = useId();
  const recipientId = useId();
  const { data } = useSWR<PlanCatalogue, ApiError>("/v1/account/plans", fetcher);
  const plans = (data?.items ?? []).filter((plan) => plan.kind !== "trial");
  const [planVersionId, setPlanVersionId] = useState("");
  const [message, setMessage] = useState("");
  const [recipient, setRecipient] = useState("");
  const [pending, setPending] = useState(false);
  const [bought, setBought] = useState<Gift | null>(null);

  async function buy() {
    const plan = plans.find((item) => item.planVersionId === planVersionId);
    if (!plan) {
      return;
    }
    setPending(true);
    try {
      const gift = await apiFetch<Gift>("/v1/account/gifts", {
        body: JSON.stringify({
          planVersionId,
          currency: plan.price.currency,
          message: message.trim(),
          recipient: recipient.trim(),
        }),
        headers: { "Idempotency-Key": crypto.randomUUID() },
        method: "POST",
      });
      setBought(gift);
      setMessage("");
      setRecipient("");
      onBought();
    } catch (failure) {
      const code = failure instanceof ApiError ? failure.code : "";
      const known = RECIPIENT_PROBLEMS.find((problem) => problem === code);
      toast.error(known ? translate(`recipientProblem.${known}`) : translate("failed"));
    } finally {
      setPending(false);
    }
  }

  if (bought) {
    return (
      <section className="space-y-3 rounded-xl border border-border bg-card p-4" role="status">
        <SectionLabel>{translate("codeTitle")}</SectionLabel>
        <p className="select-all break-all font-mono text-[18px] tracking-wider">{bought.code}</p>
        <p className="text-[12.5px] text-muted-foreground">{translate("codeHint")}</p>
        {bought.recipient && (
          <p className="text-[12.5px]">
            {translate("recipientDelivery", {
              recipient:
                bought.recipient === "email" ? translate("recipientEmail") : bought.recipient,
            })}
          </p>
        )}
        {bought.externalMinor > 0 ? (
          <Button asChild>
            <Link href={`/account/orders/${bought.orderId}`}>
              {translate("pay", { amount: money(bought.externalMinor, bought.currency) })}
            </Link>
          </Button>
        ) : (
          <p className="text-[12.5px]">{translate("paidFromWallet")}</p>
        )}
        <Button onClick={() => setBought(null)} variant="outline">
          {translate("another")}
        </Button>
      </section>
    );
  }

  return (
    <section className="space-y-3 rounded-xl border border-border bg-card p-4">
      <SectionLabel>{translate("buyTitle")}</SectionLabel>
      <p className="text-[12.5px] text-muted-foreground">{translate("buyDescription")}</p>
      <div className="space-y-1">
        <Label htmlFor={planId}>{translate("plan")}</Label>
        <Select onValueChange={setPlanVersionId} value={planVersionId}>
          <SelectTrigger id={planId}>
            <SelectValue placeholder={translate("choosePlan")} />
          </SelectTrigger>
          <SelectContent>
            {plans.map((plan) => (
              <SelectItem key={plan.planVersionId} value={plan.planVersionId}>
                {plan.name} — {money(plan.price.amountMinor, plan.price.currency)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="space-y-1">
        <Label htmlFor={recipientId}>{translate("recipient")}</Label>
        <Input
          autoComplete="off"
          id={recipientId}
          maxLength={254}
          onChange={(event) => setRecipient(event.target.value)}
          placeholder={translate("recipientPlaceholder")}
          value={recipient}
        />
        <p className="text-[12px] text-muted-foreground">{translate("recipientHint")}</p>
      </div>
      <div className="space-y-1">
        <Label htmlFor={messageId}>{translate("message")}</Label>
        <Input
          id={messageId}
          maxLength={500}
          onChange={(event) => setMessage(event.target.value)}
          value={message}
        />
      </div>
      <Button disabled={!planVersionId || pending} onClick={buy}>
        {pending ? translate("buying") : translate("buy")}
      </Button>
    </section>
  );
}
