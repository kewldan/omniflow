"use client";

import { Button } from "@omniflow/ui/button";
import { ShoppingBag } from "lucide-react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import useSWR from "swr";

import { PlanCard } from "@/components/account/commerce/plan-card";
import type { PlanCatalogue } from "@/components/account/commerce/types";
import { useOpenCheckout } from "@/components/account/commerce/use-checkout";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { type ApiError, fetcher } from "@/lib/api";

/**
 * The store: what may be bought, and what is already half-bought.
 *
 * The catalogue is the whole screen. Eligibility, the operations offered against
 * each plan, and the price in the settlement currency all arrive decided from
 * `GET /plans`, so this page sorts nothing, filters nothing, and hides nothing —
 * a plan the customer cannot start still appears, with the server's reason under
 * it, because a customer who was told about a plan and cannot find it assumes
 * the panel is broken rather than that they are ineligible.
 *
 * An unfinished checkout is surfaced at the top rather than left to be
 * rediscovered. There is only ever one, and starting another replaces it, so a
 * customer who wandered off mid-purchase should be offered the way back before
 * they unknowingly throw it away.
 */
export default function StorePage() {
  const translate = useTranslations("account.commerce");
  const { data, error, isLoading } = useSWR<PlanCatalogue, ApiError>("/v1/account/plans", fetcher);
  const { checkout } = useOpenCheckout();

  return (
    <div className="space-y-4">
      {checkout && (
        <section className="animate-step-in space-y-3 rounded-lg border border-primary/40 bg-card p-4">
          <div>
            <p className="font-semibold text-[13.5px]">{translate("store.resume.title")}</p>
            <p className="mt-1 text-[12.5px] text-muted-foreground leading-relaxed">
              {translate("store.resume.description", { plan: checkout.plan.name })}
            </p>
          </div>
          <Button asChild className="w-full" size="lg">
            <Link href="/account/checkout">{translate("store.resume.action")}</Link>
          </Button>
        </section>
      )}

      {/* The order history hangs off the store rather than off a tab of its own:
          it is where a customer goes to check a purchase they already made, and
          the store is where they were standing when they made it. */}
      <div className="flex items-baseline justify-between">
        <SectionLabel>{translate("store.plans")}</SectionLabel>
        <Link
          className="font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em] underline underline-offset-2 focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2"
          href="/account/orders"
        >
          {translate("store.orders")}
        </Link>
      </div>
      <PlanList error={error} isLoading={isLoading} plans={data?.items} />

      <ShopLink />
    </div>
  );
}

/**
 * The way into the digital-goods catalogue, offered only when there is one.
 *
 * It used to be linked unconditionally, on the reasoning that the shop page is
 * the one that knows whether anything is on sale. That is true and it is why the
 * link was wrong: an installation selling no goods advertised a shop that
 * answered "digital goods are not sold here", so the store contradicted its own
 * destination one tap later. Asking the same question this page asks about plans
 * costs one request and removes the contradiction.
 */
function ShopLink() {
  const translate = useTranslations("account.commerce");
  const { data } = useSWR<{ items: unknown[] }, ApiError>("/v1/account/shop/products", fetcher, {
    // The catalogue changes when the operator changes it, not while somebody is
    // looking at the store.
    revalidateOnFocus: false,
  });

  if (!data || data.items.length === 0) {
    return null;
  }

  return (
    <>
      <SectionLabel>{translate("store.shop.section")}</SectionLabel>
      <Link
        className="flex items-center gap-3 rounded-lg border border-border bg-card p-4 transition-colors hover:border-primary/50 focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2"
        href="/account/shop"
      >
        <span className="flex size-10 shrink-0 items-center justify-center rounded-md bg-secondary">
          <ShoppingBag aria-hidden className="size-[19px] text-muted-foreground" />
        </span>
        <span className="min-w-0">
          <span className="block font-semibold text-[15px]">{translate("store.shop.title")}</span>
          <span className="mt-0.5 block text-[12.5px] text-muted-foreground leading-relaxed">
            {translate("store.shop.description")}
          </span>
        </span>
      </Link>
    </>
  );
}

/** The catalogue with its own loading, empty, and error states. */
function PlanList({
  error,
  isLoading,
  plans,
}: {
  error?: ApiError;
  isLoading: boolean;
  plans?: PlanCatalogue["items"];
}) {
  const translate = useTranslations("account.commerce");

  if (isLoading) {
    return <ListSkeleton />;
  }
  if (error) {
    return (
      <AccountNotice
        description={translate("store.errorDescription")}
        title={translate("store.error")}
        variant="danger"
      />
    );
  }
  if (!plans || plans.length === 0) {
    return (
      <AccountNotice
        description={translate("store.emptyDescription")}
        title={translate("store.empty")}
      />
    );
  }
  return (
    <section className="space-y-3">
      {plans.map((plan) => (
        <PlanCard key={plan.planVersionId} plan={plan} />
      ))}
    </section>
  );
}
