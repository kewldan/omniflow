"use client";

import { Badge } from "@omniflow/ui/badge";
import { cn } from "@omniflow/ui/lib/utils";
import { ChevronRight } from "lucide-react";
import Link from "next/link";
import { useTranslations } from "next-intl";

import { useGoodsMeasure } from "@/components/account/shop/labels";
import type { ShopProduct } from "@/components/account/shop/types";
import { useMoney } from "@/lib/format";

/**
 * One catalogue row.
 *
 * The whole card is the link rather than a button inside it: on a phone the
 * target is a thumb, and a row with one destination should not make the
 * customer aim at a chevron.
 *
 * A product whose gateway is switched off is still shown and still opens. The
 * API returns it deliberately — somebody who bookmarked it deserves to be told
 * why it will not sell rather than to find it vanished — so the card marks it
 * and the detail screen explains it.
 */
export function ProductCard({ product }: { product: ShopProduct }) {
  const translate = useTranslations("account.shop");
  const money = useMoney();
  const measure = useGoodsMeasure();
  const label = measure(product);

  return (
    <li className="animate-rise">
      <Link
        className={cn(
          "flex items-center gap-3 rounded-lg border border-border bg-card p-4 transition-colors",
          "hover:border-primary/40 focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2",
        )}
        href={`/account/shop/${product.id}`}
      >
        <div className="min-w-0 flex-1 space-y-1.5">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="font-semibold text-[15px] tracking-[-0.01em]">{product.name}</h3>
            {label && <span className="font-mono text-[11px] text-subtle-foreground">{label}</span>}
          </div>
          {product.description && (
            <p className="line-clamp-2 text-[12.5px] text-muted-foreground leading-relaxed">
              {product.description}
            </p>
          )}
          {/* A product with no published price says so in words. Rendering a
              missing price as zero would read as "free", which is the one
              wrong thing this screen could say about money. */}
          <p
            className={cn(
              "font-mono text-[12px]",
              product.priceKnown ? "text-foreground" : "text-subtle-foreground",
            )}
          >
            {product.priceKnown && product.priceMinor !== undefined
              ? money(product.priceMinor, product.currency)
              : translate("catalogue.priceOnOpen")}
          </p>
          {!product.available && (
            <Badge variant="warning">{translate("catalogue.unavailable")}</Badge>
          )}
        </div>
        <ChevronRight aria-hidden className="size-4 shrink-0 text-subtle-foreground" />
      </Link>
    </li>
  );
}
