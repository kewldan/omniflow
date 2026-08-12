"use client";

import { cn } from "@omniflow/ui/lib/utils";
import { ChevronRight } from "lucide-react";
import Link from "next/link";
import { useFormatter, useTranslations } from "next-intl";

import { DeliveryBadge } from "@/components/account/shop/delivery-status";
import { useGoodsMeasure } from "@/components/account/shop/labels";
import { payableMinor, type ShopOrder } from "@/components/account/shop/types";
import { useMoney } from "@/lib/format";

/**
 * One purchase in the history list.
 *
 * The delivery state leads and the amount follows, because the question
 * somebody opens this list with is "did it arrive", not "what did it cost". The
 * recipient is shown here as well as on the detail screen: the customer's own
 * history is the one place that handle belongs, and it is what distinguishes
 * two otherwise identical orders bought for two different people.
 */
export function OrderCard({ order }: { order: ShopOrder }) {
  const translate = useTranslations("account.shop");
  const format = useFormatter();
  const money = useMoney();
  const measure = useGoodsMeasure();
  const label = measure(order);

  return (
    <li className="animate-rise">
      <Link
        className={cn(
          "block space-y-2 rounded-lg border border-border bg-card p-4 transition-colors",
          "hover:border-primary/40 focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2",
        )}
        href={`/account/shop/orders/${order.id}`}
      >
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0 space-y-1">
            <div className="flex flex-wrap items-baseline gap-2">
              <h3 className="font-semibold text-[15px] tracking-[-0.01em]">{order.productName}</h3>
              {label && (
                <span className="font-mono text-[11px] text-subtle-foreground">{label}</span>
              )}
              {order.quantity > 1 && (
                <span className="font-mono text-[11px] text-subtle-foreground">
                  {translate("measure.quantity", { count: order.quantity })}
                </span>
              )}
            </div>
            <p className="truncate font-mono text-[11px] text-muted-foreground">
              {order.forSelf
                ? translate("orders.self")
                : translate("orders.recipient", { handle: order.recipient })}
            </p>
          </div>
          <ChevronRight aria-hidden className="mt-1 size-4 shrink-0 text-subtle-foreground" />
        </div>

        <div className="flex flex-wrap items-center justify-between gap-2">
          <DeliveryBadge state={order.delivery.state} />
          <div className="text-right">
            <p className="font-mono font-semibold text-[13px]" data-numeric>
              {money(
                payableMinor(order.amounts.priceMinor, order.amounts.discountMinor),
                order.currency,
              )}
            </p>
            <p className="font-mono text-[10.5px] text-subtle-foreground">
              {format.dateTime(new Date(order.createdAt), {
                day: "numeric",
                month: "short",
                year: "numeric",
              })}
            </p>
          </div>
        </div>
      </Link>
    </li>
  );
}
