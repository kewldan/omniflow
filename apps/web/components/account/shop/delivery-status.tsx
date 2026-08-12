"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { LifeBuoy } from "lucide-react";
import Link from "next/link";
import { useFormatter, useTranslations } from "next-intl";

import { deliveryKey, failureKey, type ShopOrder } from "@/components/account/shop/types";
import { useMoney } from "@/lib/format";

/**
 * Which tone each delivery state renders in.
 *
 * `needs_review` is a warning rather than a failure on purpose. Nobody can yet
 * say the purchase failed — that is precisely the state's meaning — and
 * painting it red would tell the customer something untrue about their money.
 * `refunded` is informational for the mirror-image reason: the delivery failed,
 * but the outcome the customer is looking at is a resolved one.
 */
const TONE: Record<string, "danger" | "info" | "neutral" | "success" | "warning"> = {
  awaiting_payment: "warning",
  cancelled: "neutral",
  delayed: "warning",
  delivered: "success",
  failed: "danger",
  needs_review: "warning",
  polling: "info",
  queued: "info",
  refunded: "info",
  submitted: "info",
  unknown: "neutral",
};

/** The state as a pill, for a row in the history list. */
export function DeliveryBadge({ state }: { state: string }) {
  const translate = useTranslations("account.shop");
  const key = deliveryKey(state);
  return <Badge variant={TONE[key]}>{translate(`delivery.state.${key}`)}</Badge>;
}

/**
 * The honest state of one delivery.
 *
 * Everything here is read from the API on every render of the page, never from
 * anything the browser remembered: a customer who reloads mid-delivery, or
 * opens the order on another device, has to see the same thing, and the only
 * copy of that truth is the server's.
 *
 * There is no retry control, and there is not one to add. The gateway honours
 * no idempotency key, so a second submission of an ambiguous delivery is an
 * offer to buy the goods twice with the customer's money. A parked or failed
 * delivery therefore offers a person to talk to and the reference that person
 * needs, which is the only move that cannot make things worse.
 */
export function DeliveryStatus({ order }: { order: ShopOrder }) {
  const translate = useTranslations("account.shop");
  const format = useFormatter();
  const money = useMoney();
  const state = deliveryKey(order.delivery.state);

  const timestamp = (value: string) =>
    format.dateTime(new Date(value), {
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      month: "short",
    });

  return (
    <section className="space-y-3 rounded-lg border border-border bg-card p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <DeliveryBadge state={order.delivery.state} />
        {order.delivery.updatedAt && (
          <span className="font-mono text-[10.5px] text-subtle-foreground">
            {translate("order.updated", { date: timestamp(order.delivery.updatedAt) })}
          </span>
        )}
      </div>

      <p className="text-[13px] leading-relaxed">{translate(`delivery.description.${state}`)}</p>

      {/* The failure class, never the provider's message. The class is what
          decided the outcome and is something the customer can read; a gateway
          error string is noise they cannot act on. */}
      {order.delivery.failureReason && (
        <p className="text-[12.5px] text-muted-foreground leading-relaxed">
          {translate(`failure.${failureKey(order.delivery.failureReason)}`)}
        </p>
      )}

      <div className="flex flex-wrap gap-x-4 gap-y-1 font-mono text-[10.5px] text-subtle-foreground">
        {order.delivery.deliveredAt && (
          <span>
            {translate("order.deliveredAt", { date: timestamp(order.delivery.deliveredAt) })}
          </span>
        )}
        {order.delivery.attempts > 1 && (
          <span>{translate("order.attempts", { count: order.delivery.attempts })}</span>
        )}
      </div>

      {order.delivery.refund && (
        <div className="space-y-1 rounded-md border border-success/40 bg-success/10 p-3">
          <p className="font-semibold text-[13px]">{translate("order.refund.title")}</p>
          <p className="text-[12.5px] leading-relaxed">
            {translate("order.refund.amount", {
              amount: money(order.delivery.refund.amountMinor, order.delivery.refund.currency),
            })}
          </p>
        </div>
      )}

      {order.delivery.supportHandoff && (
        <div className="space-y-2.5 rounded-md border border-warning/40 bg-warning/10 p-3">
          <p className="font-semibold text-[13px]">{translate("order.support.title")}</p>
          <p className="text-[12.5px] leading-relaxed">{translate("order.support.noRetry")}</p>
          <div>
            <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
              {translate("order.support.reference")}
            </p>
            <p className="mt-1 break-all font-mono text-[12px]">
              {order.delivery.supportReference}
            </p>
          </div>
          <Button asChild className="w-full" size="lg" variant="outline">
            <Link href="/account/support">
              <LifeBuoy aria-hidden />
              {translate("order.support.action")}
            </Link>
          </Button>
        </div>
      )}
    </section>
  );
}
