"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { toast } from "@omniflow/ui/toast";
import { useParams, useRouter, useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { OPERATIONS, PlanSpecs, usePeriodLabel } from "@/components/account/commerce/plan-card";
import { useIneligibility, useProblemMessage } from "@/components/account/commerce/reasons";
import type { CheckoutView, PlanDetail } from "@/components/account/commerce/types";
import { useOpenCheckout } from "@/components/account/commerce/use-checkout";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useBytes, useMoney } from "@/lib/format";

/** The subscriptions a lifecycle change can act on, from the dashboard's own read. */
type Overview = {
  subscriptions: { id: string; label: string; plan: string }[];
  /** True only where the installation allows concurrent subscriptions. */
  showSwitcher: boolean;
};

/**
 * One plan in full, and the decision that opens a checkout for it.
 *
 * Three things are settled here and nowhere else: which lifecycle operation is
 * being started, which subscription it applies to, and — implicitly, by opening
 * a checkout — that any other unfinished checkout is being replaced. The squad
 * and add-on choices deliberately are not: those are priced, so they belong to
 * the checkout where every change comes back with a new quote attached.
 */
export default function PlanPage() {
  const translate = useTranslations("account.commerce");
  const params = useParams<{ planVersionId: string }>();
  const search = useSearchParams();
  const router = useRouter();
  const money = useMoney();
  const period = usePeriodLabel();
  const explain = useIneligibility();
  const describeProblem = useProblemMessage();

  const { data, error, isLoading } = useSWR<PlanDetail, ApiError>(
    `/v1/account/plans/${params.planVersionId}`,
    fetcher,
  );
  const { data: overview } = useSWR<Overview, ApiError>("/v1/account/overview", fetcher);
  const { checkout } = useOpenCheckout();

  const requested = search.get("operation") ?? "";
  const [operation, setOperation] = useState<string | null>(null);
  const [target, setTarget] = useState<string>("");
  const [busy, setBusy] = useState(false);

  if (isLoading) {
    return <ListSkeleton rows={3} />;
  }
  if (error || !data) {
    return (
      <AccountNotice
        description={
          error?.status === 404
            ? translate("plan.goneDescription")
            : translate("store.errorDescription")
        }
        title={error?.status === 404 ? translate("plan.gone") : translate("store.error")}
        variant="danger"
      />
    );
  }

  // The requested operation wins when the plan still offers it, which is what
  // makes a tap on "upgrade" in the catalogue mean the same thing here. It is
  // re-checked against the plan rather than trusted: the query string survives a
  // bookmark, and the customer's eligibility does not.
  const offered = data.operations;
  const preferred = offered.includes(requested) ? requested : (offered[0] ?? "");
  const selected = operation && offered.includes(operation) ? operation : preferred;

  const subscriptions = overview?.subscriptions ?? [];
  // The picker exists only where there is genuinely a choice. An installation
  // running one subscription per customer has nothing to ask, and asking anyway
  // would add a step whose only possible answer is the one already on screen.
  const asksForTarget = Boolean(overview?.showSwitcher) && subscriptions.length > 0;
  const canOpenNew = selected === "purchase";

  async function start(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    try {
      await apiFetch<CheckoutView>("/v1/account/checkout", {
        body: JSON.stringify({
          newSubscription: asksForTarget ? target === "new" : false,
          operation: selected,
          planVersionId: params.planVersionId,
          subscriptionId: asksForTarget && target !== "new" ? target : "",
        }),
        method: "POST",
      });
      router.push("/account/checkout");
    } catch (openError) {
      toast.error(describeProblem(openError));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="animate-step-in space-y-5">
      <header className="space-y-3 rounded-lg border border-border bg-card p-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <h1 className="font-semibold text-[19px] tracking-[-0.02em]">{data.name}</h1>
            {data.description && (
              <p className="mt-1.5 text-[12.5px] text-muted-foreground leading-relaxed">
                {data.description}
              </p>
            )}
          </div>
          <div className="shrink-0 text-right">
            <div className="font-bold text-[22px] leading-none tracking-[-0.03em]" data-numeric>
              {money(data.price.amountMinor, data.price.currency)}
            </div>
            <div className="mt-1 font-mono text-[10px] text-subtle-foreground">
              {period(data.billingPeriod, data.durationSeconds)}
            </div>
          </div>
        </div>
        <PlanSpecs plan={data} />
      </header>

      {!data.eligible && (
        <p
          className="rounded-lg border border-warning/40 bg-warning/10 px-4 py-3 text-[12.5px] leading-relaxed"
          role="note"
        >
          {explain(data.ineligibleReason)}
        </p>
      )}

      {data.squads.offered.length > 0 && (
        <section className="space-y-2">
          <SectionLabel>{translate("plan.squads")}</SectionLabel>
          <div className="space-y-2 rounded-lg border border-border bg-card p-4">
            <p className="text-[12.5px] text-muted-foreground leading-relaxed">
              {data.squads.configurable
                ? translate("plan.squadsConfigurable", { minimum: data.squads.minimum })
                : translate("plan.squadsAutomatic")}
            </p>
            <ul className="flex flex-wrap gap-1.5">
              {data.squads.offered.map((squad) => (
                <li key={squad.squadId}>
                  <Badge variant="outline">{squad.label}</Badge>
                </li>
              ))}
            </ul>
          </div>
        </section>
      )}

      {data.addons.length > 0 && (
        <section className="space-y-2">
          <SectionLabel>{translate("plan.addons")}</SectionLabel>
          <ul className="space-y-2">
            {data.addons.map((addon) => (
              <li
                className="flex items-start justify-between gap-3 rounded-lg border border-border bg-card p-4"
                key={addon.addonVersionId}
              >
                <div className="min-w-0">
                  <p className="font-medium text-[13.5px]">{addon.name}</p>
                  {addon.description && (
                    <p className="mt-1 text-[12px] text-muted-foreground leading-relaxed">
                      {addon.description}
                    </p>
                  )}
                  <AddonMeasures addon={addon} />
                </div>
                <span className="shrink-0 font-medium text-[13px]" data-numeric>
                  {money(addon.price.amountMinor, addon.price.currency)}
                </span>
              </li>
            ))}
          </ul>
          <p className="px-1 text-[11.5px] text-subtle-foreground leading-relaxed">
            {translate("plan.addonsHint")}
          </p>
        </section>
      )}

      {data.promotions.length > 0 && (
        <section className="space-y-2">
          <SectionLabel>{translate("plan.promotions")}</SectionLabel>
          <ul className="space-y-2">
            {data.promotions.map((promotion) => (
              <li
                className="flex items-center justify-between gap-3 rounded-lg border border-border bg-card p-4"
                key={promotion.code}
              >
                {/* A percentage campaign stores basis points, so 1000 is 10%.
                    Dividing by a hundred is unit conversion for display, not a
                    discount being computed here — what a code actually takes off
                    an order is priced by the quote and never by this page. */}
                <span className="min-w-0 text-[13px]">
                  {promotion.kind === "percent"
                    ? translate("plan.promotionPercent", { value: promotion.value / 100 })
                    : translate("plan.promotionFixed", {
                        amount: money(promotion.value, promotion.currency ?? data.price.currency),
                      })}
                </span>
                <Badge variant={promotion.eligible ? "success" : "neutral"}>
                  {translate(promotion.eligible ? "plan.promotionOpen" : "plan.promotionClosed")}
                </Badge>
              </li>
            ))}
          </ul>
        </section>
      )}

      {data.eligible && offered.length > 0 && (
        <form className="space-y-4" onSubmit={start}>
          {offered.length > 1 && (
            <fieldset className="space-y-2">
              <legend className="px-1 pb-2 font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
                {translate("plan.operation")}
              </legend>
              <div className="flex flex-wrap gap-2">
                {offered.map((candidate) => (
                  <label
                    className="cursor-pointer rounded-md border border-border bg-card px-3 py-2 text-[12.5px] has-[:checked]:border-primary has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-ring has-[:focus-visible]:outline-offset-2"
                    key={candidate}
                  >
                    <input
                      checked={candidate === selected}
                      className="sr-only"
                      name="operation"
                      onChange={() => setOperation(candidate)}
                      type="radio"
                      value={candidate}
                    />
                    {translate(
                      `operations.${OPERATIONS.includes(candidate) ? candidate : "unknown"}`,
                    )}
                  </label>
                ))}
              </div>
            </fieldset>
          )}

          {asksForTarget && (
            <fieldset className="space-y-2">
              <legend className="px-1 pb-2 font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
                {translate("plan.target")}
              </legend>
              <p className="px-1 pb-1 text-[12px] text-muted-foreground leading-relaxed">
                {translate("plan.targetHint")}
              </p>
              <div className="space-y-2">
                {canOpenNew && (
                  <TargetOption
                    checked={target === "new"}
                    label={translate("plan.targetNew")}
                    onSelect={() => setTarget("new")}
                    value="new"
                  />
                )}
                {subscriptions.map((subscription) => (
                  <TargetOption
                    checked={target === subscription.id}
                    description={subscription.plan}
                    key={subscription.id}
                    label={subscription.label}
                    onSelect={() => setTarget(subscription.id)}
                    value={subscription.id}
                  />
                ))}
              </div>
            </fieldset>
          )}

          {/* Only one checkout can be open at a time, so starting this one throws
              away whatever was in progress. Saying so before the tap is cheaper
              than explaining afterwards where the other purchase went. */}
          {checkout && checkout.planVersionId !== data.planVersionId && (
            <p
              className="rounded-lg border border-warning/40 bg-warning/10 px-4 py-3 text-[12.5px] leading-relaxed"
              role="status"
            >
              {translate("plan.replaces", { plan: checkout.plan.name })}
            </p>
          )}

          <Button className="w-full" disabled={busy} size="lg" type="submit">
            {translate("plan.start")}
          </Button>

          {data.termsUrl && (
            <p className="px-1 text-center text-[11.5px] text-subtle-foreground">
              <a
                className="underline underline-offset-2"
                href={data.termsUrl}
                rel="noopener noreferrer"
                target="_blank"
              >
                {translate("plan.terms")}
              </a>
            </p>
          )}
        </form>
      )}
    </div>
  );
}

/** What an add-on actually grants, when it grants something measurable. */
function AddonMeasures({ addon }: { addon: PlanDetail["addons"][number] }) {
  const translate = useTranslations("account.commerce");
  const formatBytes = useBytes();
  const measures: string[] = [];
  if (addon.trafficBytes !== null) {
    measures.push(translate("plan.addonTraffic", { amount: formatBytes(addon.trafficBytes) }));
  }
  if (addon.deviceSlots !== null) {
    measures.push(translate("plan.addonDevices", { count: addon.deviceSlots }));
  }
  if (addon.squadCount > 0) {
    measures.push(translate("plan.addonSquads", { count: addon.squadCount }));
  }
  if (measures.length === 0) {
    return null;
  }
  return (
    <p className="mt-1 font-mono text-[11px] text-subtle-foreground">{measures.join(" · ")}</p>
  );
}

/**
 * One subscription a change can be aimed at.
 *
 * A radio rather than a select, and with no preselection: an upgrade applied to
 * the wrong subscription is not visible until the next renewal, so the customer
 * says which one out loud rather than accepting a default the panel chose.
 */
function TargetOption({
  checked,
  description,
  label,
  onSelect,
  value,
}: {
  checked: boolean;
  description?: string;
  label: string;
  onSelect: () => void;
  value: string;
}) {
  return (
    <label className="flex cursor-pointer items-center gap-3 rounded-lg border border-border bg-card p-3 has-[:checked]:border-primary has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-ring has-[:focus-visible]:outline-offset-2">
      <input
        checked={checked}
        className="size-4 accent-[color:var(--primary)]"
        name="target"
        onChange={onSelect}
        required
        type="radio"
        value={value}
      />
      <span className="min-w-0">
        <span className="block font-medium text-[13.5px]">{label}</span>
        {description && (
          <span className="block font-mono text-[11px] text-subtle-foreground">{description}</span>
        )}
      </span>
    </label>
  );
}
