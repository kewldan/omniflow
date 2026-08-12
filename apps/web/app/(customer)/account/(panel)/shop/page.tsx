"use client";

import { Button } from "@omniflow/ui/button";
import { Receipt } from "lucide-react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import useSWR from "swr";

import { ProductCard } from "@/components/account/shop/product-card";
import { kindKey, type ShopProduct } from "@/components/account/shop/types";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { type ApiError, fetcher } from "@/lib/api";
import { groupBy } from "@/lib/format";

/**
 * The catalogue.
 *
 * No prices are quoted here, because the API quotes none: a quote is a promise
 * with an expiry on it, and a screen full of promises would start expiring
 * while it was still being read. A product with a published price shows it, and
 * one priced off a live provider rate says so in words rather than rendering a
 * missing number as zero.
 *
 * Products are grouped by what they are. Premium and Stars are bought for
 * different reasons, and a flat list mixing three-month subscriptions with
 * hundred-Star packs makes a customer read every row to find their half.
 */
export default function ShopPage() {
  const translate = useTranslations("account.shop");
  const { data, error, isLoading, mutate } = useSWR<{ items: ShopProduct[] }, ApiError>(
    "/v1/account/shop/products",
    fetcher,
  );

  if (isLoading) {
    return <ListSkeleton />;
  }
  if (error) {
    // "Not offered here" and "sold out" are different claims, and only the
    // first is true. The shop answers 503 rather than an empty list precisely
    // so this screen never says a customer should come back for stock that was
    // never going to arrive.
    if (error.code === "shop_unavailable") {
      return (
        <AccountNotice
          description={translate("states.notOfferedDescription")}
          title={translate("states.notOffered")}
          variant="offline"
        />
      );
    }
    if (error.code === "maintenance_active") {
      return (
        <AccountNotice
          description={translate("states.maintenanceDescription")}
          title={translate("states.maintenance")}
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

  const products = data?.items ?? [];
  const groups = groupBy(products, (product) => kindKey(product.kind));

  return (
    <div className="animate-step-in space-y-5">
      <header className="space-y-1">
        <h1 className="font-semibold text-[19px] tracking-[-0.02em]">{translate("title")}</h1>
        <p className="text-[13px] text-muted-foreground leading-relaxed">{translate("subtitle")}</p>
      </header>

      <Button asChild className="w-full justify-start" size="lg" variant="outline">
        <Link href="/account/shop/orders">
          <Receipt aria-hidden />
          {translate("catalogue.orders")}
        </Link>
      </Button>

      {products.length === 0 ? (
        <AccountNotice
          description={translate("states.emptyDescription")}
          title={translate("states.empty")}
        />
      ) : (
        groups.map(([kind, items]) => (
          <section className="space-y-3" key={kind}>
            <SectionLabel>{translate(`kinds.${kind}`)}</SectionLabel>
            <ul className="space-y-3">
              {items.map((product) => (
                <ProductCard key={product.id} product={product} />
              ))}
            </ul>
          </section>
        ))
      )}
    </div>
  );
}
