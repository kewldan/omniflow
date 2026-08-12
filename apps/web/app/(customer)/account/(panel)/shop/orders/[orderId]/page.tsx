"use client";

import { Button } from "@omniflow/ui/button";
import { CreditCard } from "lucide-react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useFormatter, useTranslations } from "next-intl";
import useSWR from "swr";

import { DeliveryStatus } from "@/components/account/shop/delivery-status";
import { useGoodsMeasure } from "@/components/account/shop/labels";
import { isDeliveryInFlight, type ShopOrder } from "@/components/account/shop/types";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { type ApiError, fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";

/** How often an order that is still moving is re-read. */
const POLL_MS = 8_000;

/**
 * One purchase in full: what was bought, where it went, what it cost, and where
 * its delivery has got to.
 *
 * The delivery state is polled while it is still moving and left alone once it
 * settles, so an order opened on a phone updates itself without the customer
 * pulling to refresh, and a finished one costs nothing to leave on screen. Every
 * figure comes from that read: nothing about progress is remembered in the
 * browser, so reloading the page, or opening it on another device, shows the
 * same thing rather than a hopeful local guess.
 */
export default function ShopOrderPage() {
  const translate = useTranslations("account.shop");
  const format = useFormatter();
  const money = useMoney();
  const measure = useGoodsMeasure();
  const params = useParams<{ orderId: string }>();

  const { data, error, isLoading, mutate } = useSWR<ShopOrder, ApiError>(
    `/v1/account/shop/orders/${params.orderId}`,
    fetcher,
    {
      refreshInterval: (order) => (order && isDeliveryInFlight(order.delivery.state) ? POLL_MS : 0),
    },
  );

  if (isLoading) {
    return <ListSkeleton rows={3} />;
  }
  if (error || !data) {
    if (error?.code === "not_found" || error?.status === 404) {
      return (
        <AccountNotice
          action={
            <Button asChild variant="outline">
              <Link href="/account/shop/orders">{translate("orders.title")}</Link>
            </Button>
          }
          description={translate("states.notFoundDescription")}
          title={translate("states.notFound")}
        />
      );
    }
    return (
      <AccountNotice
        action={<Button onClick={() => mutate()}>{translate("actions.retry")}</Button>}
        description={translate("states.orderErrorDescription")}
        title={translate("states.orderError")}
        variant="danger"
      />
    );
  }

  const label = measure(data);
  const timestamp = (value: string) =>
    format.dateTime(new Date(value), {
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      month: "short",
      year: "numeric",
    });

  return (
    <div className="animate-step-in space-y-4">
      <header className="space-y-1.5">
        <div className="flex flex-wrap items-baseline gap-2">
          <h1 className="font-semibold text-[19px] tracking-[-0.02em]">{data.productName}</h1>
          {label && <span className="font-mono text-[12px] text-subtle-foreground">{label}</span>}
          {data.quantity > 1 && (
            <span className="font-mono text-[12px] text-subtle-foreground">
              {translate("measure.quantity", { count: data.quantity })}
            </span>
          )}
        </div>
        <p className="font-mono text-[10.5px] text-subtle-foreground">
          {translate("order.placed", { date: timestamp(data.createdAt) })}
        </p>
      </header>

      <DeliveryStatus order={data} />

      {/* An unpaid order is settled through the one checkout the whole panel
          uses. The shop deliberately opens no payment of its own: a second
          checkout is how two checkouts start disagreeing about what is
          enabled. */}
      {data.payment.required && (
        <section className="space-y-2.5 rounded-lg border border-warning/40 bg-warning/10 p-4">
          <p className="font-semibold text-[13.5px]">
            {data.payment.possible
              ? translate("order.payment.required")
              : translate("order.payment.impossible", { currency: data.currency })}
          </p>
          <p className="text-[12.5px] leading-relaxed">
            {data.payment.possible
              ? translate("order.payment.requiredDescription")
              : translate("order.payment.impossibleDescription")}
          </p>
          {data.payment.possible ? (
            <Button asChild className="w-full" size="lg">
              <Link href={`/account/orders/${data.id}`}>
                <CreditCard aria-hidden />
                {translate("order.payment.pay")}
              </Link>
            </Button>
          ) : (
            <Button asChild className="w-full" size="lg" variant="outline">
              <Link href="/account/support">{translate("order.support.action")}</Link>
            </Button>
          )}
        </section>
      )}

      <section className="space-y-2 rounded-lg border border-border bg-card p-4">
        <SectionLabel>{translate("order.recipientTitle")}</SectionLabel>
        <p className="break-all font-mono font-semibold text-[15px]">@{data.recipient}</p>
        {data.forSelf && (
          <p className="font-mono text-[11px] text-subtle-foreground">{translate("orders.self")}</p>
        )}
      </section>

      <section className="space-y-2 rounded-lg border border-border bg-card p-4">
        <SectionLabel>{translate("order.amountsTitle")}</SectionLabel>
        <dl className="space-y-1.5">
          <Amount
            label={translate("order.amounts.price")}
            value={money(data.amounts.priceMinor, data.currency)}
          />
          {data.amounts.discountMinor > 0 && (
            <Amount
              label={translate("order.amounts.discount")}
              value={`−${money(data.amounts.discountMinor, data.currency)}`}
            />
          )}
          {data.amounts.walletMinor > 0 && (
            <Amount
              label={translate("order.amounts.wallet")}
              value={money(data.amounts.walletMinor, data.currency)}
            />
          )}
          {data.amounts.externalMinor > 0 && (
            <Amount
              label={translate("order.amounts.external")}
              value={money(data.amounts.externalMinor, data.currency)}
            />
          )}
          <Amount
            label={translate("order.amounts.paid")}
            value={money(data.amounts.paidMinor, data.currency)}
          />
        </dl>
        <p className="font-mono text-[10.5px] text-subtle-foreground">
          {translate("order.updated", { date: timestamp(data.updatedAt) })}
        </p>
      </section>

      <Button asChild className="w-full" size="lg" variant="outline">
        <Link href="/account/shop">{translate("actions.back")}</Link>
      </Button>
    </div>
  );
}

function Amount({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="text-[12.5px] text-muted-foreground">{label}</dt>
      <dd className="font-mono text-[12.5px]" data-numeric>
        {value}
      </dd>
    </div>
  );
}
