"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { TableCell, TableRow } from "@omniflow/ui/table";
import Link from "next/link";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { PageHeader, ResourceTable } from "@/components/admin/resource-table";
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
  const [reason, setReason] = useState("");
  const { run, pending, error } = useOperatorAction();

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

      {can("gifts.write") && (
        <Card className="p-3">
          <Input
            onChange={(event) => setReason(event.target.value)}
            placeholder={translate("reasonPlaceholder")}
            value={reason}
          />
          {error && <p className="mt-2 text-danger-foreground text-xs">{error.message}</p>}
        </Card>
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
              {new Date(gift.createdAt).toLocaleString(locale)}
            </TableCell>
            <TableCell>
              {translate(`kind.${gift.kind}`)}
              {gift.creditMinor ? ` · ${formatMoney(gift.creditMinor, gift.currency, locale)}` : ""}
            </TableCell>
            <TableCell className="font-mono text-[12px]">…{gift.codeHint}</TableCell>
            <TableCell className="font-mono text-[11px]">
              <Link
                className="underline-offset-2 hover:underline"
                href={`/admin/customers/${gift.senderId}`}
              >
                {gift.senderId.slice(0, 8)}
              </Link>
            </TableCell>
            <TableCell>
              <Badge variant={STATUS_VARIANT[gift.status] ?? "neutral"}>
                {translate(`status.${gift.status}`)}
              </Badge>
            </TableCell>
            <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
              {new Date(gift.expiresAt).toLocaleDateString(locale)}
            </TableCell>
            {can("gifts.write") && (
              <TableCell>
                <div className="flex gap-2">
                  {/* A claimed gift is deliberately not revocable: the recipient
                      already holds what it bought, and taking it back is a
                      refund decision made against their entitlement. */}
                  <Button
                    disabled={
                      pending ||
                      reason.trim().length === 0 ||
                      !["pending", "deliverable", "expired"].includes(gift.status)
                    }
                    onClick={async () => {
                      if (
                        await run(`/v1/panel/gifts/${gift.id}/revoke`, {
                          method: "POST",
                          reason: reason.trim(),
                        })
                      ) {
                        setReason("");
                        await mutate();
                      }
                    }}
                    size="sm"
                    variant="destructive"
                  >
                    {translate("actions.revoke")}
                  </Button>
                  <Button
                    disabled={pending || !["revoked", "expired"].includes(gift.status)}
                    onClick={async () => {
                      if (
                        await run(`/v1/panel/gifts/${gift.id}/refund`, {
                          method: "POST",
                          reason: reason.trim(),
                        })
                      ) {
                        await mutate();
                      }
                    }}
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
    </div>
  );
}
