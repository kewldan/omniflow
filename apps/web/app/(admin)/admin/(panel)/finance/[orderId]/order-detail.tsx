"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import Link from "next/link";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import { formatMoney, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

type PaymentEvent = {
  type: string;
  previousStatus?: string;
  status?: string;
  amountMinor?: number;
  currency?: string;
  occurredAt: string;
};

type PaymentIntent = {
  id: string;
  provider: string;
  status: string;
  amountMinor: number;
  currency: string;
  providerReference?: string;
  createdAt: string;
  updatedAt: string;
  events?: PaymentEvent[] | null;
};

type Refund = {
  id: string;
  paymentIntentId: string;
  status: string;
  amountMinor: number;
  currency: string;
  reason: string;
  createdAt: string;
};

type OrderDetail = {
  order: {
    id: string;
    state: string;
    operation: string;
    currency: string;
    subtotalMinor: number;
    discountMinor: number;
    walletMinor: number;
    externalMinor: number;
    paidMinor: number;
    refundedMinor: number;
    customerId: string;
    createdAt: string;
  };
  intents: PaymentIntent[] | null;
  refunds: Refund[] | null;
};

/**
 * One order, with every payment attempt and refund against it.
 *
 * The provider payload is deliberately absent. It is retained for replay and
 * dispute and reachable through the webhook diagnostics with its own
 * permission; putting it here would put a payment payload in front of every
 * operator who can read an order.
 */
export function OrderDetailView({ orderId }: { orderId: string }) {
  const translate = useTranslations("admin.finance");
  const locale = useLocale();
  const { can } = useSession();

  const { data, error, isLoading, mutate } = useSWR<OrderDetail, ApiError>(
    `/v1/panel/finance/orders/${orderId}`,
    fetcher,
  );

  if (error) {
    return (
      <StateNotice
        title={error.status === 404 ? translate("detail.notFound") : translate("detail.loadFailed")}
        variant={error.status === 404 ? "empty" : "danger"}
      />
    );
  }
  if (isLoading || !data) {
    return <Skeleton className="h-64 w-full" />;
  }

  const { order } = data;
  const refundable = Math.max(order.paidMinor - order.refundedMinor, 0);

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        description={translate("detail.description")}
        eyebrow={translate("title")}
        title={<span className="font-mono text-xl">{order.id.slice(0, 8)}</span>}
      />

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            {order.operation}
            <Badge variant="neutral">{order.state}</Badge>
          </CardTitle>
          <CardDescription>
            {new Date(order.createdAt).toLocaleString(locale)} ·{" "}
            <Link
              className="underline-offset-2 hover:underline"
              href={`/admin/customers/${order.customerId}`}
            >
              {order.customerId.slice(0, 8)}
            </Link>
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap gap-x-8 gap-y-3">
          <Amount
            currency={order.currency}
            label={translate("detail.subtotal")}
            locale={locale}
            value={order.subtotalMinor}
          />
          <Amount
            currency={order.currency}
            label={translate("detail.discount")}
            locale={locale}
            value={order.discountMinor}
          />
          {/* Wallet and external are shown apart because they answer different
              questions: what the customer's balance covered, and what a
              provider actually settled. */}
          <Amount
            currency={order.currency}
            label={translate("detail.wallet")}
            locale={locale}
            value={order.walletMinor}
          />
          <Amount
            currency={order.currency}
            label={translate("detail.external")}
            locale={locale}
            value={order.externalMinor}
          />
          <Amount
            currency={order.currency}
            label={translate("detail.paid")}
            locale={locale}
            value={order.paidMinor}
          />
          <Amount
            currency={order.currency}
            label={translate("detail.refunded")}
            locale={locale}
            value={order.refundedMinor}
          />
        </CardContent>
      </Card>

      {can("finance.write") && refundable > 0 && (
        <RefundForm
          currency={order.currency}
          maximumMinor={refundable}
          onDone={() => mutate()}
          orderId={order.id}
        />
      )}

      <section aria-labelledby="intents-heading" className="flex flex-col gap-3">
        <h2 className="font-semibold text-[15px] tracking-tight" id="intents-heading">
          {translate("detail.intents")}
        </h2>
        {(data.intents ?? []).length === 0 ? (
          <StateNotice title={translate("detail.noIntents")} variant="empty" />
        ) : (
          (data.intents ?? []).map((intent) => (
            <Card className="flex flex-col gap-3 p-4" key={intent.id}>
              <div className="flex flex-wrap items-center justify-between gap-3">
                <span className="flex items-center gap-2">
                  <span className="font-medium">{intent.provider}</span>
                  <Badge variant={intent.status === "succeeded" ? "success" : "neutral"}>
                    {intent.status}
                  </Badge>
                </span>
                <span className="flex items-center gap-3">
                  <span className="tabular-nums">
                    {formatMoney(intent.amountMinor, intent.currency, locale)}
                  </span>
                  {can("finance.write") && (
                    <ReconcileButton intentId={intent.id} onDone={() => mutate()} />
                  )}
                </span>
              </div>

              {/* The event vocabulary is the readable part of a payment
                  timeline: a mismatch, a duplicate, a late settlement, and an
                  overpayment each say what happened rather than leaving an
                  operator to infer it from two status values. */}
              <ol className="flex flex-col gap-1 border-border border-l pl-3">
                {(intent.events ?? []).map((event) => (
                  <li
                    className="flex flex-wrap justify-between gap-2 text-sm"
                    key={`${event.type}-${event.occurredAt}`}
                  >
                    <span>
                      {translate(`events.${event.type}`)}
                      {event.previousStatus && event.status
                        ? ` · ${event.previousStatus} → ${event.status}`
                        : ""}
                    </span>
                    <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
                      {new Date(event.occurredAt).toLocaleString(locale)}
                    </span>
                  </li>
                ))}
              </ol>
            </Card>
          ))
        )}
      </section>

      {(data.refunds ?? []).length > 0 && (
        <section aria-labelledby="refunds-heading" className="flex flex-col gap-3">
          <h2 className="font-semibold text-[15px] tracking-tight" id="refunds-heading">
            {translate("detail.refunds")}
          </h2>
          <Card className="flex flex-col gap-2 p-4">
            {(data.refunds ?? []).map((refund) => (
              <div
                className="flex flex-wrap items-baseline justify-between gap-3 text-sm"
                key={refund.id}
              >
                <span className="flex items-center gap-2">
                  <Badge variant={refund.status === "succeeded" ? "success" : "neutral"}>
                    {refund.status}
                  </Badge>
                  <span className="text-muted-foreground">{refund.reason}</span>
                </span>
                <span className="tabular-nums">
                  {formatMoney(refund.amountMinor, refund.currency, locale)}
                </span>
              </div>
            ))}
          </Card>
        </section>
      )}
    </div>
  );
}

function Amount({
  currency,
  label,
  locale,
  value,
}: {
  currency: string;
  label: string;
  locale: string;
  value: number;
}) {
  return (
    <span className="flex flex-col">
      <span className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
        {label}
      </span>
      <span className="font-medium tabular-nums">{formatMoney(value, currency, locale)}</span>
    </span>
  );
}

/**
 * Re-polls a provider for one payment intent.
 *
 * The poll is performed by the idempotent payment service; this records who
 * asked. The panel never decides how a payment settles, which is why the
 * response is "accepted" rather than a new status.
 */
function ReconcileButton({ intentId, onDone }: { intentId: string; onDone: () => void }) {
  const translate = useTranslations("admin.finance");
  const { run, pending } = useOperatorAction();
  return (
    <Button
      disabled={pending}
      onClick={async () => {
        const ok = await run(`/v1/panel/finance/payments/${intentId}/reconcile`, {
          body: { outcome: "requested" },
          method: "POST",
        });
        if (ok) {
          onDone();
        }
      }}
      size="sm"
      variant="outline"
    >
      {translate("detail.reconcile")}
    </Button>
  );
}

/**
 * Records a refund decision against an order.
 *
 * The amount is capped at what is still refundable, so an operator cannot
 * record more than was ever paid. The refund itself is executed by the payment
 * service against the provider's own capabilities — full or partial, supported
 * or not — and this captures the decision and its reason, which the refund
 * record does not otherwise carry.
 */
function RefundForm({
  currency,
  maximumMinor,
  onDone,
  orderId,
}: {
  currency: string;
  maximumMinor: number;
  onDone: () => void;
  orderId: string;
}) {
  const translate = useTranslations("admin.finance");
  const locale = useLocale();
  const amountId = useId();
  const reasonId = useId();
  const [amount, setAmount] = useState(String(maximumMinor));
  const [reason, setReason] = useState("");
  const { run, pending, error } = useOperatorAction();

  const requested = Number(amount);
  const valid = Number.isFinite(requested) && requested > 0 && requested <= maximumMinor;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("detail.refundTitle")}</CardTitle>
        <CardDescription>
          {translate("detail.refundable", {
            amount: formatMoney(maximumMinor, currency, locale),
          })}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={amountId}>{translate("detail.refundAmount")}</Label>
            <Input
              id={amountId}
              inputMode="numeric"
              onChange={(event) => setAmount(event.target.value)}
              value={amount}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={reasonId}>{translate("detail.refundReason")}</Label>
            <Input
              id={reasonId}
              onChange={(event) => setReason(event.target.value)}
              placeholder={translate("detail.refundReasonPlaceholder")}
              value={reason}
            />
          </div>
        </div>
        <p className="text-muted-foreground text-xs">{translate("detail.minorUnitsHint")}</p>
        {error && <p className="text-danger-foreground text-sm">{error.message}</p>}
        <Button
          className="self-start"
          disabled={pending || !valid || reason.trim().length === 0}
          onClick={async () => {
            const ok = await run(`/v1/panel/finance/orders/${orderId}/refund`, {
              body: { amountMinor: requested, currency },
              method: "POST",
              reason: reason.trim(),
            });
            if (ok) {
              setReason("");
              onDone();
            }
          }}
          size="sm"
          variant="destructive"
        >
          {translate("detail.issueRefund")}
        </Button>
      </CardContent>
    </Card>
  );
}
