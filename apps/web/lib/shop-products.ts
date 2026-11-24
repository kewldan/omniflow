"use client";

import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";

type Listed = { id: string; kind: string };

/**
 * The shop's catalogue, read once and shared by the menu, the store, and the
 * shop page. The catalogue changes when the operator changes it, not while
 * somebody is looking at it.
 */
export function useShopProducts(): Listed[] | undefined {
  const { data } = useSWR<{ items: Listed[] }, ApiError>("/v1/account/shop/products", fetcher, {
    revalidateOnFocus: false,
    shouldRetryOnError: false,
  });
  return data?.items;
}

/** Where "the shop" leads: straight to the product when there is only one. */
export function shopHref(products: Listed[] | undefined): string {
  return products?.length === 1 ? `/account/shop/${products[0]?.id}` : "/account/shop";
}

/** The Stars product, when the shop sells one. */
export function starsProduct(products: Listed[] | undefined): Listed | undefined {
  return products?.find((product) => product.kind === "telegram_stars");
}
