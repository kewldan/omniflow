"use client";

import { Button } from "@omniflow/ui/button";
import Link from "next/link";
import { useTranslations } from "next-intl";
import useSWRInfinite from "swr/infinite";

import { OrderCard } from "@/components/account/shop/order-card";
import type { ShopOrderPage } from "@/components/account/shop/types";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { type ApiError, fetcher, toQuery } from "@/lib/api";

const PAGE_SIZE = 20;

/**
 * The cursor for the page after `previous`.
 *
 * Returning null ends the list, which is how SWR's infinite hook learns there
 * is nothing more to ask for. The cursor is the created-at timestamp *and* the
 * order identifier because two orders can share a millisecond, and a cursor
 * that cannot break that tie either repeats a row or skips one.
 */
function pageKey(index: number, previous: ShopOrderPage | null): string | null {
  if (previous && !previous.nextCursor) {
    return null;
  }
  if (index === 0 || !previous) {
    return `/v1/account/shop/orders${toQuery({ limit: PAGE_SIZE })}`;
  }
  return `/v1/account/shop/orders${toQuery({
    cursor: previous.nextCursor,
    cursorId: previous.nextCursorId,
    limit: PAGE_SIZE,
  })}`;
}

/**
 * The customer's purchase history.
 *
 * Paging is a button rather than a scroll listener. A customer who came here to
 * check one order should be able to reach the end of a page and stop, and an
 * order list that grows as you scroll makes the footer of the app unreachable
 * on a phone.
 */
export default function ShopOrdersPage() {
  const translate = useTranslations("account.shop");
  const { data, error, isLoading, isValidating, mutate, setSize, size } = useSWRInfinite<
    ShopOrderPage,
    ApiError
  >(pageKey, fetcher, { revalidateFirstPage: false });

  if (isLoading) {
    return <ListSkeleton />;
  }
  if (error) {
    if (error.code === "shop_unavailable") {
      return (
        <AccountNotice
          description={translate("states.notOfferedDescription")}
          title={translate("states.notOffered")}
          variant="offline"
        />
      );
    }
    return (
      <AccountNotice
        action={<Button onClick={() => mutate()}>{translate("actions.retry")}</Button>}
        description={translate("states.errorDescription")}
        title={translate("states.error")}
        variant="danger"
      />
    );
  }

  const pages = data ?? [];
  const orders = pages.flatMap((page) => page.items);
  const hasMore = Boolean(pages.at(-1)?.nextCursor);
  // A page still in flight leaves an undefined slot behind, which is what
  // separates "loading the next page" from an ordinary background revalidation.
  const loadingMore = isValidating && size > 0 && data?.[size - 1] === undefined;

  if (orders.length === 0) {
    return (
      <AccountNotice
        action={
          <Button asChild>
            <Link href="/account/shop">{translate("orders.browse")}</Link>
          </Button>
        }
        description={translate("orders.emptyDescription")}
        title={translate("orders.empty")}
      />
    );
  }

  return (
    <div className="animate-step-in space-y-4">
      <SectionLabel>{translate("orders.title")}</SectionLabel>

      <ul aria-busy={loadingMore} className="space-y-3">
        {orders.map((order) => (
          <OrderCard key={order.id} order={order} />
        ))}
      </ul>

      {hasMore && (
        <Button
          className="w-full"
          disabled={loadingMore}
          onClick={() => setSize(size + 1)}
          size="lg"
          variant="outline"
        >
          {loadingMore ? translate("orders.loading") : translate("orders.loadMore")}
        </Button>
      )}
    </div>
  );
}
