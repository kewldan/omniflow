"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { TableCell, TableRow } from "@omniflow/ui/table";
import Link from "next/link";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { PageHeader, ResourceTable } from "@/components/admin/resource-table";
import { type ApiError, fetcher, toQuery } from "@/lib/api";
import { type Page, type PersonalOffer, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";
import { useUrlFilters } from "@/lib/use-url-filters";

import { OfferComposer } from "./offer-composer";

const STATUSES = ["", "active", "redeemed", "dismissed", "expired", "revoked"] as const;

/**
 * Targeted offers, and the form that creates them.
 *
 * A personal offer is a promotion pointed at one customer. It carries its own
 * copy in both languages and its own validity window, and it is redeemable
 * once — which is why the register shows the resolution rather than only the
 * status: "redeemed on an order" and "expired unused" are the two answers a
 * marketing operator is actually looking for.
 */
export function OfferRegister() {
  const translate = useTranslations("admin.offers");
  const locale = useLocale();
  const { can } = useSession();
  const [composing, setComposing] = useState(false);

  const { filters, setFilter, reset } = useUrlFilters(["status", "customerId"]);
  const query = toQuery({
    customerId: filters.customerId,
    pageSize: 50,
    status: filters.status,
  });
  const { data, error, isLoading, mutate } = useSWR<Page<PersonalOffer>, ApiError>(
    `/v1/panel/offers${query}`,
    fetcher,
  );

  const items = data?.items ?? [];

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        actions={
          can("marketing.write") ? (
            <Button onClick={() => setComposing((open) => !open)} size="sm">
              {translate(composing ? "close" : "create")}
            </Button>
          ) : null
        }
        description={translate("description")}
        title={translate("title")}
      />

      {composing && (
        <OfferComposer
          onCreated={() => {
            setComposing(false);
            void mutate();
          }}
        />
      )}

      <div className="flex flex-wrap items-end gap-2">
        {STATUSES.map((status) => (
          <Button
            key={status || "all"}
            onClick={() => setFilter("status", status)}
            size="sm"
            variant={filters.status === status ? "default" : "outline"}
          >
            {translate(`status.${status || "all"}`)}
          </Button>
        ))}
        <Button className="ml-auto" onClick={reset} size="sm" variant="ghost">
          {translate("resetFilters")}
        </Button>
      </div>

      <ResourceTable
        columns={[
          translate("columns.title"),
          translate("columns.customer"),
          translate("columns.window"),
          translate("columns.status"),
          "",
        ]}
        emptyDescription={translate("empty.description")}
        emptyTitle={translate("empty.title")}
        error={error}
        filtersActive={filters.status !== "" || filters.customerId !== ""}
        loading={isLoading}
        onClearFilters={reset}
        renderRow={(offer) => (
          <TableRow key={offer.id}>
            <TableCell className="max-w-[22rem]">
              <span className="font-medium">{offer.titleEn}</span>
              <span className="block text-muted-foreground text-xs">{offer.titleRu}</span>
            </TableCell>
            <TableCell className="font-mono text-[11px]">
              <Link
                className="underline-offset-2 hover:underline"
                href={`/admin/customers/${offer.customerId}`}
              >
                {offer.customerId.slice(0, 8)}
              </Link>
            </TableCell>
            <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
              {new Date(offer.startsAt).toLocaleDateString(locale)}
              {" → "}
              {new Date(offer.expiresAt).toLocaleDateString(locale)}
            </TableCell>
            <TableCell>
              <Badge
                variant={
                  offer.status === "redeemed"
                    ? "success"
                    : offer.status === "active"
                      ? "neutral"
                      : "warning"
                }
              >
                {translate(`status.${offer.status}`)}
              </Badge>
              {offer.orderId && (
                <Link
                  className="ml-2 font-mono text-[11px] underline-offset-2 hover:underline"
                  href={`/admin/finance/${offer.orderId}`}
                >
                  {offer.orderId.slice(0, 8)}
                </Link>
              )}
            </TableCell>
            <TableCell>
              {can("marketing.write") && offer.status === "active" && (
                <RevokeOffer offerId={offer.id} onDone={() => mutate()} />
              )}
            </TableCell>
          </TableRow>
        )}
        rows={items}
      />
    </div>
  );
}

/**
 * Withdraws an offer that has not been used.
 *
 * A redeemed offer cannot be revoked: the customer already bought at that
 * price, and the record of what they were offered is what makes the order
 * explicable afterwards.
 */
function RevokeOffer({ offerId, onDone }: { offerId: string; onDone: () => void }) {
  const translate = useTranslations("admin.offers");
  const { run, pending } = useOperatorAction();
  const [reason, setReason] = useState("");

  return (
    <span className="flex items-center gap-2">
      <input
        aria-label={translate("revokeReason")}
        className="h-8 w-40 rounded-md border border-border bg-transparent px-2 text-sm"
        onChange={(event) => setReason(event.target.value)}
        placeholder={translate("revokeReason")}
        value={reason}
      />
      <Button
        disabled={pending || reason.trim().length === 0}
        onClick={async () => {
          const ok = await run(`/v1/panel/offers/${offerId}/revoke`, {
            method: "POST",
            reason: reason.trim(),
          });
          if (ok) {
            setReason("");
            onDone();
          }
        }}
        size="sm"
        variant="ghost"
      >
        {translate("revoke")}
      </Button>
    </span>
  );
}
