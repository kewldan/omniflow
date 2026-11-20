"use client";

import { Button } from "@omniflow/ui/button";
import { ExternalLink } from "lucide-react";
import { useTranslations } from "next-intl";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";

/**
 * The self-employed («Мой налог») receipt for a paid order, when the operator
 * issues them. Renders nothing otherwise, including on installations where
 * the route does not exist.
 */
export function TaxReceiptLink({ orderId }: { orderId: string }) {
  const translate = useTranslations("account.commerce.order");
  const { data } = useSWR<{ receiptUrl: string }, ApiError>(
    `/v1/account/orders/${orderId}/tax-receipt`,
    fetcher,
    { shouldRetryOnError: false },
  );
  if (!data?.receiptUrl?.startsWith("https://")) {
    return null;
  }
  return (
    <Button asChild className="w-full" size="lg" variant="outline">
      <a href={data.receiptUrl} rel="noreferrer noopener" target="_blank">
        <ExternalLink aria-hidden />
        {translate("taxReceipt")}
      </a>
    </Button>
  );
}
