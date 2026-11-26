"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { TableCell, TableRow } from "@omniflow/ui/table";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { CustomerChip } from "@/components/admin/customer-chip";
import { ReasonDialog } from "@/components/admin/reason-dialog";
import { PageHeader, ResourceTable } from "@/components/admin/resource-table";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher, toQuery } from "@/lib/api";
import { formatMoney, type Gift, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";
import { useUrlFilters } from "@/lib/use-url-filters";

type GiftPage = { items: Gift[] | null; nextCursor?: string; totals: Record<string, number> };

const STATUS_VARIANT: Record<string, "success" | "warning" | "danger" | "neutral"> = {
  claimed: "success",
  deliverable: "neutral",
  expired: "warning",
  pending: "neutral",
  refunded: "neutral",
  revoked: "danger",
};

/**
 * The gift register.
 *
 * The claim code is never shown, because only its digest is stored. The four
 * characters in the hint column are enough for a support operator to confirm
 * they are looking at the gift a sender is asking about, and not enough to
 * redeem it.
 */
export function GiftRegister() {
  const translate = useTranslations("admin.gifts");
  const locale = useLocale();
  const { can } = useSession();
  const revokeReasons = useTranslations("admin.reasons.giftRevoke");
  const refundReasons = useTranslations("admin.reasons.giftRefund");
  const { run, pending, error } = useOperatorAction();
  const [deciding, setDeciding] = useState<{ gift: Gift; kind: "revoke" | "refund" } | null>(null);

  const { filters, cursorStack, setFilter, reset, goNext, goPrevious, hasPrevious } = useUrlFilters(
    ["status"],
  );

  const query = toQuery({ cursor: filters.cursor, status: filters.status });
  const {
    data,
    error: loadError,
    isLoading,
    isValidating,
    mutate,
  } = useSWR<GiftPage, ApiError>(`/v1/panel/gifts${query}`, fetcher, { keepPreviousData: true });

  const totals = data?.totals ?? {};

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />

      {/* The status filter only means something once there are gifts: an
          empty register would show a lone "All" pill above "Nothing here yet". */}
      {Object.keys(totals).length > 0 && (
        <div className="flex flex-wrap gap-2">
          <Button
            onClick={() => setFilter("status", "")}
            size="sm"
            variant={filters.status ? "ghost" : "outline"}
          >
            {translate("all")}
          </Button>
          {Object.entries(totals).map(([status, count]) => (
            <Button
              key={status}
              onClick={() => setFilter("status", status)}
              size="sm"
              variant={filters.status === status ? "outline" : "ghost"}
            >
              {translate(`status.${status}`)} · {count}
            </Button>
          ))}
        </div>
      )}

      <ResourceTable<Gift>
        columns={[
          translate("columns.createdAt"),
          translate("columns.kind"),
          translate("columns.hint"),
          translate("columns.sender"),
          translate("columns.status"),
          translate("columns.expiresAt"),
          ...(can("gifts.write") ? [translate("columns.actions")] : []),
        ]}
        error={loadError}
        filtersActive={Boolean(filters.status)}
        loading={isLoading}
        onClearFilters={reset}
        onRetry={() => mutate()}
        pagination={{
          hasNext: Boolean(data?.nextCursor),
          hasPrevious,
          onNext: () => goNext(data?.nextCursor),
          onPrevious: goPrevious,
          page: cursorStack.length + 1,
        }}
        renderRow={(gift) => (
          <TableRow key={gift.id}>
            <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
              <Timestamp value={gift.createdAt} />
            </TableCell>
            <TableCell>
              {translate(`kind.${gift.kind}`)}
              {gift.creditMinor ? ` · ${formatMoney(gift.creditMinor, gift.currency, locale)}` : ""}
            </TableCell>
            <TableCell className="font-mono text-[12px]">…{gift.codeHint}</TableCell>
            <TableCell>
              <CustomerChip id={gift.senderId} />
            </TableCell>
            <TableCell>
              <Badge variant={STATUS_VARIANT[gift.status] ?? "neutral"}>
                {translate(`status.${gift.status}`)}
              </Badge>
            </TableCell>
            <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
              <Timestamp mode="date" value={gift.expiresAt} />
            </TableCell>
            {can("gifts.write") && (
              <TableCell>
                <div className="flex gap-2">
                  {/* A claimed gift is deliberately not revocable: the recipient
                      already holds what it bought, and taking it back is a
                      refund decision made against their entitlement. */}
                  <Button
                    disabled={!["pending", "deliverable", "expired"].includes(gift.status)}
                    onClick={() => setDeciding({ gift, kind: "revoke" })}
                    size="sm"
                    variant="destructive"
                  >
                    {translate("actions.revoke")}
                  </Button>
                  <Button
                    disabled={!["revoked", "expired"].includes(gift.status)}
                    onClick={() => setDeciding({ gift, kind: "refund" })}
                    size="sm"
                    variant="outline"
                  >
                    {translate("actions.refund")}
                  </Button>
                </div>
              </TableCell>
            )}
          </TableRow>
        )}
        rows={data?.items ?? undefined}
        validating={isValidating}
      />

      <ReasonDialog
        confirmLabel={translate(deciding?.kind === "refund" ? "actions.refund" : "actions.revoke")}
        description={translate(
          deciding?.kind === "refund" ? "refundDescription" : "revokeDescription",
          { hint: deciding?.gift.codeHint ?? "" },
        )}
        destructive={deciding?.kind !== "refund"}
        error={error?.message}
        key={deciding ? `${deciding.kind}-${deciding.gift.id}` : "closed"}
        onConfirm={async (reason) => {
          if (
            deciding &&
            (await run(`/v1/panel/gifts/${deciding.gift.id}/${deciding.kind}`, {
              method: "POST",
              reason,
            }))
          ) {
            setDeciding(null);
            await mutate();
          }
        }}
        onOpenChange={(open) => !open && setDeciding(null)}
        open={deciding !== null}
        pending={pending}
        presets={
          deciding?.kind === "refund"
            ? ["afterRevoke", "expiredUnclaimed", "providerRefund"].map((key) => refundReasons(key))
            : ["disputedPayment", "senderRequest", "wrongRecipient", "codeLeaked"].map((key) =>
                revokeReasons(key),
              )
        }
        title={translate(deciding?.kind === "refund" ? "refundTitle" : "revokeTitle")}
      />
    </div>
  );
}
