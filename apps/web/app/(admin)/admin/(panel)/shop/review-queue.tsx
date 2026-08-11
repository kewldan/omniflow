"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Skeleton } from "@omniflow/ui/skeleton";
import Link from "next/link";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import { formatMoney, type Listing, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

type ParkedDelivery = {
  orderId: string;
  customerId: string;
  providerSlug: string;
  recipient: string;
  priceMinor: number;
  currency: string;
  attempts: number;
  errorCode?: string;
  updatedAt: string;
};

/**
 * Deliveries nobody could resolve automatically.
 *
 * The gateway honours no idempotency key, so an answer that never arrived means
 * the goods may or may not have been sent. Retrying could deliver and charge
 * twice; refunding could give money back for goods the recipient received.
 * Neither is safe, so the delivery waits here until somebody checks with the
 * provider and says which happened.
 *
 * The evidence is outside Omniflow, which is why this screen records a verdict
 * rather than offering a "retry" that would guess.
 */
export function ReviewQueue({ active }: { active: boolean }) {
  const translate = useTranslations("admin.shop");
  const locale = useLocale();
  const { can } = useSession();
  const [reason, setReason] = useState("");
  const { run, pending, error } = useOperatorAction();

  const { data, isLoading, mutate } = useSWR<Listing<ParkedDelivery>, ApiError>(
    active ? "/v1/panel/goods/review" : null,
    fetcher,
  );

  async function resolve(orderId: string, delivered: boolean) {
    const ok = await run(`/v1/panel/goods/orders/${orderId}/resolve`, {
      body: { delivered },
      method: "POST",
      reason: reason.trim(),
    });
    if (ok) {
      setReason("");
      await mutate();
    }
  }

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <Card className="p-6">
        <StateNotice
          description={translate("empty.reviewDescription")}
          title={translate("empty.review")}
          variant="empty"
        />
      </Card>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      <Card className="border-warning/40 bg-warning/5 p-3 text-sm">
        {translate("reviewExplainer")}
      </Card>

      {can("goods.write") && (
        <Card className="p-3">
          <Input
            onChange={(event) => setReason(event.target.value)}
            placeholder={translate("reviewReasonPlaceholder")}
            value={reason}
          />
          {error && <p className="mt-2 text-danger-foreground text-xs">{error.message}</p>}
        </Card>
      )}

      <div className="grid gap-3 lg:grid-cols-2">
        {items.map((delivery) => (
          <Card className="flex flex-col gap-3 p-4" key={delivery.orderId}>
            <div className="flex items-start justify-between gap-3">
              <div className="flex flex-col gap-0.5">
                <span className="font-medium">@{delivery.recipient}</span>
                <span className="font-mono text-[11px] text-muted-foreground">
                  {delivery.providerSlug} · {new Date(delivery.updatedAt).toLocaleString(locale)}
                </span>
              </div>
              <Badge variant="warning">{translate("failure.ambiguous")}</Badge>
            </div>

            <dl className="grid grid-cols-2 gap-1 text-sm">
              <div className="flex flex-col">
                <dt className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
                  {translate("columns.price")}
                </dt>
                <dd className="tabular-nums">
                  {formatMoney(delivery.priceMinor, delivery.currency, locale)}
                </dd>
              </div>
              <div className="flex flex-col">
                <dt className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
                  {translate("review.attempts")}
                </dt>
                <dd className="tabular-nums">{delivery.attempts}</dd>
              </div>
            </dl>

            {delivery.errorCode && (
              <p className="font-mono text-danger-foreground text-xs">{delivery.errorCode}</p>
            )}

            <Link
              className="font-mono text-[11px] underline-offset-2 hover:underline"
              href={`/admin/customers/${delivery.customerId}`}
            >
              {delivery.customerId.slice(0, 8)}
            </Link>

            {can("goods.write") && (
              <div className="flex gap-2">
                <Button
                  disabled={pending || reason.trim().length === 0}
                  onClick={() => resolve(delivery.orderId, true)}
                  size="sm"
                  variant="outline"
                >
                  {translate("review.confirmDelivered")}
                </Button>
                <Button
                  disabled={pending || reason.trim().length === 0}
                  onClick={() => resolve(delivery.orderId, false)}
                  size="sm"
                  variant="destructive"
                >
                  {translate("review.refund")}
                </Button>
              </div>
            )}
          </Card>
        ))}
      </div>
    </div>
  );
}
