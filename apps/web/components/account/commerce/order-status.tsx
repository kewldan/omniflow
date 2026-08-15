"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import { ExternalLink, MessageSquare, ShieldCheck, Wallet } from "lucide-react";
import Link from "next/link";
import { useFormatter, useTranslations } from "next-intl";
import type { ReactNode } from "react";

import type {
  OrderFulfillment,
  OrderPayment,
  OrderRefund,
  OrderSummary,
} from "@/components/account/commerce/types";
import { useMoney } from "@/lib/format";

/**
 * The order's state, as the customer needs to read it.
 *
 * `phase` is the single value every screen here branches on. It is folded on the
 * server by `commerce.EvaluatePaymentPhase` from three separate facts — the order
 * state, the payment intent's status, and the fulfillment operation's status —
 * precisely so that neither panel has to combine them itself. A late webhook
 * arriving after provisioning already completed is the case that makes this
 * worth insisting on: the order says "pending", the payment says "pending", and
 * the truth is "completed", and only the server sees all three at once.
 */

/** Every phase `EvaluatePaymentPhase` can return. */
const PHASES = [
  "awaiting_action",
  "pending",
  "succeeded",
  "provisioning",
  "completed",
  "failed",
  "cancelled",
  "expired",
  "refunded",
];

const PHASE_TONE: Record<string, "danger" | "info" | "neutral" | "success" | "warning"> = {
  awaiting_action: "warning",
  cancelled: "neutral",
  completed: "success",
  expired: "neutral",
  failed: "danger",
  pending: "warning",
  provisioning: "info",
  refunded: "neutral",
  succeeded: "success",
};

/** Narrows a phase to one the copy covers, so an unseen value still renders. */
export function knownPhase(phase: string): string {
  return PHASES.includes(phase) ? phase : "unknown";
}

/** The order's phase as a status pill. */
export function OrderPhaseBadge({ phase }: { phase: string }) {
  const translate = useTranslations("account.commerce");
  const key = knownPhase(phase);
  return (
    <Badge variant={PHASE_TONE[key] ?? "neutral"}>{translate(`order.phase.${key}.label`)}</Badge>
  );
}

/**
 * The three stages of a purchase, drawn from the server's phase alone.
 *
 * This is the part that has to survive a reload, a second tab, and a customer
 * who paid in Telegram and came back to the browser. It does, because nothing
 * here is remembered by React: the stage is read from the order every time the
 * component renders, and the order reads it from the fulfillment operation in
 * the database. A progress bar driven by client state would show a fresh page
 * "waiting to start" while provisioning was already half done.
 */
export function OrderProgress({ order }: { order: OrderSummary }) {
  const translate = useTranslations("account.commerce");
  const phase = knownPhase(order.phase);

  const reached: Record<string, number> = {
    awaiting_action: 0,
    cancelled: 0,
    completed: 3,
    expired: 0,
    failed: 1,
    pending: 0,
    provisioning: 2,
    refunded: 3,
    succeeded: 2,
    unknown: 0,
  };
  const done = reached[phase] ?? 0;
  const steps = [
    translate("order.progress.paid"),
    translate("order.progress.provisioning"),
    translate("order.progress.ready"),
  ];
  const summary = translate("order.progress.summary", { done, total: steps.length });

  return (
    <div className="space-y-2">
      <p className="font-mono text-[11px] text-muted-foreground">{summary}</p>
      <ol aria-label={summary} className="flex gap-1.5">
        {steps.map((step, index) => (
          <li className="flex-1 space-y-1" key={step}>
            <span
              aria-hidden
              className={cn(
                "block h-1 rounded-full transition-colors duration-500 motion-reduce:transition-none",
                index < done ? "bg-primary" : "bg-muted",
                phase === "failed" && index === done && "bg-destructive",
              )}
            />
            <span
              className={cn(
                "block font-mono text-[9.5px]",
                index < done ? "text-foreground" : "text-subtle-foreground",
              )}
            >
              {step}
            </span>
          </li>
        ))}
      </ol>
    </div>
  );
}

/**
 * What the provisioning worker is doing, when it has begun.
 *
 * The attempt count is shown once there has been more than one, because a
 * retrying operation looks identical to a stuck one from outside and the
 * difference is the only thing a waiting customer wants to know. The error code
 * is the worker's own bounded value, never a provider message — those can carry
 * identifiers that have no business on a customer's screen.
 */
export function FulfillmentNote({ fulfillment }: { fulfillment: OrderFulfillment }) {
  const translate = useTranslations("account.commerce");
  const format = useFormatter();
  const statuses = ["pending", "running", "succeeded", "retrying", "failed", "cancelled"];
  const status = statuses.includes(fulfillment.status) ? fulfillment.status : "unknown";

  return (
    <div className="space-y-1 rounded-lg border border-border bg-card p-4">
      <p className="font-semibold text-[13.5px]">{translate("order.fulfillment.title")}</p>
      <p className="text-[12.5px] text-muted-foreground leading-relaxed">
        {translate(`order.fulfillment.status.${status}`)}
      </p>
      {fulfillment.attempts > 1 && (
        <p className="font-mono text-[11px] text-subtle-foreground">
          {translate("order.fulfillment.attempts", { count: fulfillment.attempts })}
        </p>
      )}
      {fulfillment.updatedAt && (
        <p className="font-mono text-[11px] text-subtle-foreground">
          {translate("order.fulfillment.updated", {
            time: format.dateTime(new Date(fulfillment.updatedAt), {
              day: "numeric",
              hour: "2-digit",
              minute: "2-digit",
              month: "short",
            }),
          })}
        </p>
      )}
    </div>
  );
}

const HANDOFF_ICON: Record<string, ReactNode> = {
  hosted: <ExternalLink aria-hidden className="size-[18px]" />,
  manual: <ShieldCheck aria-hidden className="size-[18px]" />,
  none: <Wallet aria-hidden className="size-[18px]" />,
  telegram_invoice: <MessageSquare aria-hidden className="size-[18px]" />,
};

/**
 * How this payment is finished, and where.
 *
 * The four handoffs behave nothing alike and each needs its own sentence. A
 * hosted page is somewhere the customer must go; a Telegram invoice arrives on
 * its own and the browser is the wrong place to wait for it; a manual transfer
 * ends with an operator approving it, which can take hours; and `none` means the
 * wallet already covered the order and there is nothing to pay. Collapsing them
 * into one "complete your payment" message would leave three of the four
 * customers waiting for something that is never going to appear.
 */
export function PaymentHandoff({ payment }: { payment: OrderPayment }) {
  const translate = useTranslations("account.commerce");
  const kind = payment.handoff in HANDOFF_ICON ? payment.handoff : "none";

  return (
    <section className="space-y-3 rounded-lg border border-border bg-card p-4">
      <div className="flex items-start gap-3">
        <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-secondary text-muted-foreground">
          {HANDOFF_ICON[kind]}
        </span>
        <div className="min-w-0 space-y-1">
          <p className="font-semibold text-[13.5px]">{translate(`order.handoff.${kind}.title`)}</p>
          <p className="text-[12.5px] text-muted-foreground leading-relaxed">
            {translate(`order.handoff.${kind}.description`)}
          </p>
        </div>
      </div>

      {/* The provider's page is opened by a link the customer presses rather than
          by a redirect: a popup fired from an async handler is blocked by every
          modern browser, and a customer who came back and needs the page again
          must be able to reach it a second time. */}
      {payment.checkoutUrl && (
        <Button asChild className="w-full" size="lg">
          <a href={payment.checkoutUrl} rel="noopener noreferrer" target="_blank">
            <ExternalLink aria-hidden />
            {translate(`order.handoff.${kind}.action`)}
          </a>
        </Button>
      )}
      {/* A manual transfer with nowhere to go is the one handoff that can leave
          the customer holding an amount and no way to send it. The operator
          approves the transfer by hand, so the way to ask where to send it is the
          same desk that will approve it. */}
      {kind === "manual" && !payment.checkoutUrl && (
        <Button asChild className="w-full" size="lg" variant="outline">
          <Link href="/account/support/new">{translate("order.handoff.manual.ask")}</Link>
        </Button>
      )}
      {payment.receiptUrl && (
        <Button asChild className="w-full" size="lg" variant="outline">
          <a href={payment.receiptUrl} rel="noopener noreferrer" target="_blank">
            {translate("order.receipt")}
          </a>
        </Button>
      )}
    </section>
  );
}

/** The order's money, line by line, as the order itself records it. */
export function OrderAmounts({ order }: { order: OrderSummary }) {
  const translate = useTranslations("account.commerce");
  const money = useMoney();
  const amount = (minor: number) => money(minor, order.currency);

  const rows: { label: string; value: string }[] = [
    { label: translate("breakdown.subtotal"), value: amount(order.subtotalMinor) },
  ];
  if (order.discountMinor !== 0) {
    rows.push({ label: translate("breakdown.discount"), value: `−${amount(order.discountMinor)}` });
  }
  if (order.walletMinor !== 0) {
    rows.push({ label: translate("breakdown.wallet"), value: `−${amount(order.walletMinor)}` });
  }
  rows.push({ label: translate("breakdown.external"), value: amount(order.externalMinor) });
  if (order.paidMinor !== 0) {
    rows.push({ label: translate("order.amounts.paid"), value: amount(order.paidMinor) });
  }
  if (order.refundedMinor !== 0) {
    rows.push({ label: translate("order.amounts.refunded"), value: amount(order.refundedMinor) });
  }

  return (
    <dl className="space-y-2">
      {rows.map((row) => (
        <div className="flex items-baseline justify-between gap-3" key={row.label}>
          <dt className="text-[12.5px] text-muted-foreground">{row.label}</dt>
          <dd className="font-medium text-[13px]" data-numeric>
            {row.value}
          </dd>
        </div>
      ))}
    </dl>
  );
}

/** Refunds recorded against the order, newest first. */
export function RefundList({ refunds }: { refunds: OrderRefund[] }) {
  const translate = useTranslations("account.commerce");
  const format = useFormatter();
  const money = useMoney();
  const statuses = ["pending", "processing", "succeeded", "failed", "cancelled"];

  return (
    <ul className="space-y-2">
      {refunds.map((refund) => {
        const status = statuses.includes(refund.status) ? refund.status : "unknown";
        return (
          <li
            className="flex items-baseline justify-between gap-3 rounded-md border border-border bg-card p-3"
            key={`${refund.createdAt}-${refund.amountMinor}`}
          >
            <div className="min-w-0">
              <p className="font-medium text-[12.5px]">
                {translate(`order.refundStatus.${status}`)}
              </p>
              <p className="font-mono text-[11px] text-subtle-foreground">
                {format.dateTime(new Date(refund.createdAt), {
                  day: "numeric",
                  month: "short",
                  year: "numeric",
                })}
              </p>
            </div>
            <span className="font-medium text-[13px]" data-numeric>
              {money(refund.amountMinor, refund.currency)}
            </span>
          </li>
        );
      })}
    </ul>
  );
}
