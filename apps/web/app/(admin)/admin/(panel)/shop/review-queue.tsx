"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { CustomerChip } from "@/components/admin/customer-chip";
import { useEnumLabel } from "@/components/admin/enum-label";
import { ReasonDialog } from "@/components/admin/reason-dialog";
import { StateNotice } from "@/components/admin/state-notice";
import { Timestamp } from "@/components/intl/timestamp";
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
  const label = useEnumLabel();
  const locale = useLocale();
  const { can } = useSession();
  const deliveredReasons = useTranslations("admin.reasons.goodsDelivered");
  const refundReasons = useTranslations("admin.reasons.goodsRefund");
  const { run, pending, error } = useOperatorAction();
  const [resolving, setResolving] = useState<{
    delivery: ParkedDelivery;
    delivered: boolean;
  } | null>(null);

  const { data, isLoading, mutate } = useSWR<Listing<ParkedDelivery>, ApiError>(
    active ? "/v1/panel/goods/review" : null,
    fetcher,
  );

  async function resolve(reason: string) {
    if (!resolving) {
      return;
    }
    const ok = await run(`/v1/panel/goods/orders/${resolving.delivery.orderId}/resolve`, {
      body: { delivered: resolving.delivered },
      method: "POST",
      reason,
    });
    if (ok) {
      setResolving(null);
      await mutate();
    }
  }

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <StateNotice
        description={translate("empty.reviewDescription")}
        title={translate("empty.review")}
        variant="empty"
      />
    );
  }

  return (
    <div className="flex flex-col gap-3">
      <Card className="border-warning/40 bg-warning/5 p-3 text-sm">
        {translate("reviewExplainer")}
      </Card>

      <div className="grid gap-3 lg:grid-cols-2">
        {items.map((delivery) => (
          <Card className="flex flex-col gap-3 p-4" key={delivery.orderId}>
            <div className="flex items-start justify-between gap-3">
              <div className="flex flex-col gap-0.5">
                <span className="font-medium">@{delivery.recipient}</span>
                <span className="font-mono text-[11px] text-muted-foreground">
                  {label("goodsProvider", delivery.providerSlug)} ·{" "}
                  <Timestamp value={delivery.updatedAt} />
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

            <CustomerChip id={delivery.customerId} />

            {can("goods.manage") && (
              <div className="flex gap-2">
                <Button
                  onClick={() => setResolving({ delivered: true, delivery })}
                  size="sm"
                  variant="outline"
                >
                  {translate("review.confirmDelivered")}
                </Button>
                <Button
                  onClick={() => setResolving({ delivered: false, delivery })}
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

      <ReasonDialog
        confirmLabel={translate(resolving?.delivered ? "review.confirmDelivered" : "review.refund")}
        description={
          resolving
            ? translate(
                resolving.delivered ? "review.deliveredDescription" : "review.refundDescription",
                {
                  amount: formatMoney(
                    resolving.delivery.priceMinor,
                    resolving.delivery.currency,
                    locale,
                  ),
                  recipient: resolving.delivery.recipient,
                },
              )
            : undefined
        }
        destructive={resolving?.delivered === false}
        error={error?.message}
        key={resolving ? `${resolving.delivery.orderId}-${resolving.delivered}` : "closed"}
        onConfirm={resolve}
        onOpenChange={(open) => !open && setResolving(null)}
        open={resolving !== null}
        pending={pending}
        presets={
          resolving?.delivered
            ? ["providerConfirmed", "recipientConfirmed", "providerHistory"].map((key) =>
                deliveredReasons(key),
              )
            : ["providerNotSent", "providerRejected", "recipientInvalid", "unconfirmed"].map(
                (key) => refundReasons(key),
              )
        }
        title={translate(resolving?.delivered ? "review.deliveredTitle" : "review.refundTitle")}
      />
    </div>
  );
}
