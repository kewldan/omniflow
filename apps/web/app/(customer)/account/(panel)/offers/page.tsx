"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { toast } from "@omniflow/ui/toast";
import Link from "next/link";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { AccountNotice, ListSkeleton } from "@/components/account/state";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { formatMoney } from "@/lib/money";

type Offer = {
  id: string;
  title: string;
  terms: string;
  promoCode?: string;
  expiresAt: string;
  activatedAt?: string;
};

type OfferTaken = { activated: boolean; expiresAt: string; bonusMinor: number; currency?: string };

const OFFERS_KEY = "/v1/account/offers";

/**
 * The customer's personal offers.
 *
 * Checkout applies the best active offer by itself, so this screen never asks
 * for a code. It is where a customer sees what they were offered, takes an
 * offer — which starts a timed offer's clock and pays its bonus once — and
 * drops one they do not want, which also resets that discount at checkout.
 */
export default function OffersPage() {
  const translate = useTranslations("account.offers");
  const locale = useLocale();
  const { data, error, isLoading, mutate } = useSWR<{ items: Offer[] }, ApiError>(
    OFFERS_KEY,
    fetcher,
  );
  const [busy, setBusy] = useState<string | null>(null);
  const [dismissing, setDismissing] = useState<string | null>(null);

  async function take(offer: Offer) {
    setBusy(offer.id);
    try {
      const taken = await apiFetch<OfferTaken>(`/v1/account/offers/${offer.id}/take`, {
        method: "POST",
      });
      if (taken.activated && taken.bonusMinor > 0 && taken.currency) {
        toast.success(
          translate("bonusCredited", {
            amount: formatMoney(taken.bonusMinor, taken.currency, locale),
          }),
        );
      } else {
        toast.success(translate("taken"));
      }
      await mutate();
    } catch (takeError) {
      toast.error((takeError as ApiError).message);
    } finally {
      setBusy(null);
    }
  }

  async function dismiss(id: string) {
    setBusy(id);
    try {
      await apiFetch(`/v1/account/offers/${id}/dismiss`, { method: "POST" });
      toast.success(translate("dismissed"));
      await mutate();
    } catch (dismissError) {
      toast.error((dismissError as ApiError).message);
    } finally {
      setBusy(null);
      setDismissing(null);
    }
  }

  if (isLoading) {
    return <ListSkeleton rows={2} />;
  }
  if (error) {
    return (
      <AccountNotice description={translate("failedDescription")} title={translate("failed")} />
    );
  }
  const offers = data?.items ?? [];
  if (offers.length === 0) {
    return <AccountNotice description={translate("emptyDescription")} title={translate("empty")} />;
  }

  return (
    <div className="animate-step-in space-y-3">
      <p className="px-1 text-[13px] text-muted-foreground">{translate("intro")}</p>
      <ul className="space-y-3">
        {offers.map((offer) => (
          <li
            className="space-y-3 rounded-xl border border-border bg-card px-4 py-4"
            key={offer.id}
          >
            <div className="flex items-start justify-between gap-3">
              <div className="space-y-1">
                <div className="font-medium text-[15px]">{offer.title}</div>
                {offer.terms && (
                  <p className="whitespace-pre-line text-[13px] text-muted-foreground">
                    {offer.terms}
                  </p>
                )}
              </div>
              {offer.activatedAt ? (
                <Badge variant="success">{translate("active")}</Badge>
              ) : (
                <Badge variant="neutral">{translate("new")}</Badge>
              )}
            </div>
            <div className="font-mono text-[11px] text-subtle-foreground">
              {translate("until")} <Timestamp value={offer.expiresAt} />
            </div>
            <div className="flex flex-wrap gap-2">
              {offer.activatedAt ? (
                <Button asChild size="sm">
                  <Link href="/account/store">{translate("use")}</Link>
                </Button>
              ) : (
                <Button disabled={busy !== null} onClick={() => take(offer)} size="sm">
                  {translate("take")}
                </Button>
              )}
              <Button
                disabled={busy !== null}
                onClick={() => setDismissing(offer.id)}
                size="sm"
                variant="ghost"
              >
                {translate("dismiss")}
              </Button>
            </div>
          </li>
        ))}
      </ul>

      <ConfirmDialog
        cancelLabel={translate("keep")}
        confirmLabel={translate("dismiss")}
        description={translate("dismissDescription")}
        destructive
        onConfirm={() => dismissing && dismiss(dismissing)}
        onOpenChange={(open) => !open && setDismissing(null)}
        open={dismissing !== null}
        pending={busy !== null}
        title={translate("dismissTitle")}
      />
    </div>
  );
}
