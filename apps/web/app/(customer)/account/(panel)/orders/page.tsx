"use client";

import { Button } from "@omniflow/ui/button";
import Link from "next/link";
import { useFormatter, useTranslations } from "next-intl";
import useSWRInfinite from "swr/infinite";

import { OrderPhaseBadge } from "@/components/account/commerce/order-status";
import { OPERATIONS } from "@/components/account/commerce/plan-card";
import type { OrderPage, OrderSummary } from "@/components/account/commerce/types";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { type ApiError, fetcher, toQuery } from "@/lib/api";
import { useMoney } from "@/lib/format";

/**
 * Everything the customer has ever ordered.
 *
 * The history is cursor-paginated on the pair the list is ordered by, so a
 * payment that settles between two pages cannot make a row appear twice or drop
 * one out of sight. Pages accumulate rather than replacing one another: this is
 * a history someone scrolls back through, and a numbered pager would make
 * "further back" feel like navigation away from what they were reading.
 */
export default function OrdersPage() {
  const translate = useTranslations("account.commerce");
  const { data, error, isLoading, isValidating, setSize, size } = useSWRInfinite<
    OrderPage,
    ApiError
  >((index, previous) => {
    if (index > 0 && !previous?.nextCursor) {
      return null;
    }
    return `/v1/account/orders${toQuery({
      cursor: index === 0 ? undefined : previous?.nextCursor,
      cursorId: index === 0 ? undefined : previous?.nextCursorId,
      limit: 20,
    })}`;
  }, fetcher);

  if (isLoading) {
    return <ListSkeleton />;
  }
  if (error) {
    return (
      <AccountNotice
        description={translate("store.errorDescription")}
        title={translate("store.error")}
        variant="danger"
      />
    );
  }

  const pages = data ?? [];
  const orders = pages.flatMap((page) => page.items);
  // The last page having a cursor is the server's own statement that more exist.
  // Comparing counts to a page size would be this panel guessing at the API's
  // limit, which it does not set and cannot see.
  const more = Boolean(pages[pages.length - 1]?.nextCursor);

  if (orders.length === 0) {
    return (
      <AccountNotice
        action={
          <Button asChild>
            <Link href="/account/store">{translate("checkout.browse")}</Link>
          </Button>
        }
        description={translate("orders.emptyDescription")}
        title={translate("orders.empty")}
      />
    );
  }

  return (
    <div className="space-y-4">
      <SectionLabel>{translate("orders.title")}</SectionLabel>
      <ul aria-busy={isValidating} className="space-y-3">
        {orders.map((order) => (
          <li key={order.id}>
            <OrderRow order={order} />
          </li>
        ))}
      </ul>
      {more && (
        <Button
          className="w-full"
          disabled={isValidating}
          onClick={() => setSize(size + 1)}
          size="lg"
          variant="outline"
        >
          {translate("actions.loadMore")}
        </Button>
      )}
    </div>
  );
}

/** One order as a row: what it was, what it cost, and where it got to. */
function OrderRow({ order }: { order: OrderSummary }) {
  const translate = useTranslations("account.commerce");
  const format = useFormatter();
  const money = useMoney();
  const operation = OPERATIONS.includes(order.operation) ? order.operation : "unknown";

  return (
    <Link
      className="flex animate-rise items-start gap-3 rounded-lg border border-border bg-card p-4 transition-colors hover:border-primary/50 focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2"
      href={`/account/orders/${order.id}`}
    >
      <span className="min-w-0 flex-1 space-y-1.5">
        <span className="flex items-center gap-2">
          <OrderPhaseBadge phase={order.phase} />
          <span className="truncate font-mono text-[11px] text-subtle-foreground">
            {translate(`operations.${operation}`)}
          </span>
        </span>
        <span className="block truncate font-semibold text-[15px]">{order.plan}</span>
        <span className="block font-mono text-[11px] text-subtle-foreground">
          {format.dateTime(new Date(order.createdAt), {
            day: "numeric",
            month: "short",
            year: "numeric",
          })}
        </span>
        {order.refundedMinor > 0 && (
          <span className="block font-mono text-[11px] text-warning">
            {translate("orders.refunded", {
              amount: money(order.refundedMinor, order.currency),
            })}
          </span>
        )}
      </span>
      {/* The subtotal is shown as the order's figure and the paid amount beside
          it, because both are recorded on the order. Subtracting one from
          another to print a "total" would be this panel doing arithmetic on
          money, which is the server's job and the one thing the two must never
          disagree about. */}
      <span className="shrink-0 space-y-1 text-right">
        <span className="block font-semibold text-[15px]" data-numeric>
          {money(order.subtotalMinor, order.currency)}
        </span>
        {order.paidMinor > 0 && (
          <span className="block font-mono text-[10px] text-subtle-foreground">
            {translate("orders.paid", { amount: money(order.paidMinor, order.currency) })}
          </span>
        )}
        {order.payment?.receiptUrl && (
          <span className="block font-mono text-[10px] text-subtle-foreground">
            {translate("orders.hasReceipt")}
          </span>
        )}
      </span>
    </Link>
  );
}
