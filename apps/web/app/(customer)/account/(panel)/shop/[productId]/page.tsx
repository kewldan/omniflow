"use client";

import { useParams } from "next/navigation";

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
  return <PurchaseForm productId={params.productId} />;
}
