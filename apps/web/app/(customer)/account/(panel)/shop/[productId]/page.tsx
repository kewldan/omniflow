"use client";

import { Button } from "@omniflow/ui/button";
import { Receipt } from "lucide-react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useTranslations } from "next-intl";

import { PurchaseForm } from "@/components/account/shop/purchase-form";

/**
 * One product, and the purchase built around it.
 *
 * The route reads the identifier and nothing else: quoting, the recipient step,
 * the promotion, and the confirmation are one flow whose parts constrain each
 * other, so they live together in a single component rather than being spread
 * across a page that would have to hold their shared state anyway.
 */
export default function ShopProductPage() {
  const params = useParams<{ productId: string }>();
  const translate = useTranslations("account.shop");
  // The catalogue page that carried this link is skipped when the shop sells
  // one product, so the product page carries it instead.
  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Button asChild size="sm" variant="ghost">
          <Link href="/account/shop/orders">
            <Receipt aria-hidden />
            {translate("catalogue.orders")}
          </Link>
        </Button>
      </div>
      <PurchaseForm productId={params.productId} />
    </div>
  );
}
