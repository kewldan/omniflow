"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { useCallback } from "react";

import { useIneligibility } from "@/components/account/commerce/reasons";
import type { PlanOffer } from "@/components/account/commerce/types";
import { useBytes, useMoney } from "@/lib/format";

/**
 * How a plan is described, and how a refusal is explained.
 *
 * The catalogue answers three questions at once: what does this cost, what do I
 * get, and may I have it. The third is the one a panel must never answer for
 * itself — `eligible`, `ineligibleReason`, and `operations` are computed by
 * `applyEligibility` on the server against the customer's own subscriptions and
 * trial history. This file renders that verdict; it never derives one. A card
 * that decided for itself which buttons to show would be a second copy of the
 * rules in `internal/commerce`, and the two would eventually disagree — in the
 * customer's favour or against it, both of which are bugs.
 */

/** The billing periods the catalogue can carry, from `plan_versions`. */
const PERIODS = ["none", "day", "week", "month", "quarter", "half_year", "year", "custom"];

/** The lifecycle operations `applyEligibility` can offer. */
export const OPERATIONS = ["purchase", "extension", "upgrade", "downgrade"];

/**
 * Names the period a plan covers.
 *
 * A named period is shown by name, because "monthly" is what the customer
 * compares plans on. A custom or unnamed one falls back to the duration in whole
 * days, which is the only honest reading of an interval nobody gave a name to.
 */
export function usePeriodLabel(): (billingPeriod: string, durationSeconds: number) => string {
  const translate = useTranslations("account.commerce");
  return useCallback(
    (billingPeriod: string, durationSeconds: number) => {
      const days = Math.max(0, Math.round(durationSeconds / 86_400));
      if (
        !PERIODS.includes(billingPeriod) ||
        billingPeriod === "custom" ||
        billingPeriod === "none"
      ) {
        return translate("period.days", { count: days });
      }
      return translate(`period.${billingPeriod}`);
    },
    [translate],
  );
}

/**
 * The plan's measurable facts as a definition list.
 *
 * A definition list rather than a grid of divs so a screen reader reads "traffic,
 * 200 GB" as one pair instead of two loose strings, and so the same markup can
 * be scanned visually in two columns on a phone.
 */
export function PlanSpecs({ plan }: { plan: PlanOffer }) {
  const translate = useTranslations("account.commerce");
  const formatBytes = useBytes();
  const period = usePeriodLabel();

  const rows: { label: string; value: string }[] = [
    { label: translate("specs.period"), value: period(plan.billingPeriod, plan.durationSeconds) },
    {
      label: translate("specs.traffic"),
      value:
        plan.trafficAllowanceBytes === null
          ? translate("specs.unlimited")
          : formatBytes(plan.trafficAllowanceBytes),
    },
    {
      label: translate("specs.devices"),
      value:
        plan.deviceLimit === null
          ? translate("specs.unlimited")
          : translate("specs.deviceCount", { count: plan.deviceLimit }),
    },
  ];
  if (plan.gracePeriodSeconds > 0) {
    rows.push({
      label: translate("specs.grace"),
      value: translate("period.days", { count: Math.round(plan.gracePeriodSeconds / 86_400) }),
    });
  }

  return (
    <dl className="grid grid-cols-2 gap-x-4 gap-y-2">
      {rows.map((row) => (
        <div className="flex items-baseline justify-between gap-2" key={row.label}>
          <dt className="font-mono text-[10.5px] text-subtle-foreground uppercase tracking-[0.1em]">
            {row.label}
          </dt>
          <dd className="text-right font-medium text-[12.5px]" data-numeric>
            {row.value}
          </dd>
        </div>
      ))}
    </dl>
  );
}

/**
 * One plan in the comparison list.
 *
 * The lifecycle operations are links rather than buttons that open a checkout
 * directly: a renewal, an upgrade, or a downgrade has to name the subscription
 * it changes before it can be opened at all, and the plan page is where that
 * choice is made. Sending the intent along in the query keeps the tap from the
 * catalogue meaningful without pretending the decision is already complete.
 */
export function PlanCard({ plan }: { plan: PlanOffer }) {
  const translate = useTranslations("account.commerce");
  const money = useMoney();
  const period = usePeriodLabel();
  const explain = useIneligibility();

  return (
    <article
      className={cn(
        "animate-step-in space-y-4 rounded-lg border border-border bg-card p-4",
        // The plan the customer is on is the one they are comparing everything
        // else against, so it is marked rather than left to be inferred from a
        // button label.
        plan.held && "border-foreground/30 ring-1 ring-foreground/10",
        !plan.eligible && "opacity-80",
      )}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="truncate font-semibold text-[17px] tracking-[-0.01em]">{plan.name}</h3>
          {plan.description && (
            <p className="mt-1 text-[12.5px] text-muted-foreground leading-relaxed">
              {plan.description}
            </p>
          )}
        </div>
        <div className="shrink-0 text-right">
          <div className="font-bold text-[20px] leading-none tracking-[-0.03em]" data-numeric>
            {money(plan.price.amountMinor, plan.price.currency)}
          </div>
          <div className="mt-1 font-mono text-[10px] text-subtle-foreground">
            {period(plan.billingPeriod, plan.durationSeconds)}
          </div>
        </div>
      </div>

      <div className="flex flex-wrap gap-1.5">
        {plan.held && <Badge variant="success">{translate("kind.current")}</Badge>}
        {plan.kind === "trial" && <Badge variant="info">{translate("kind.trial")}</Badge>}
        {plan.recurringCapable && <Badge variant="outline">{translate("kind.recurring")}</Badge>}
        {plan.configurableSquads && <Badge variant="outline">{translate("kind.squads")}</Badge>}
      </div>

      <PlanSpecs plan={plan} />

      {plan.eligible ? (
        <div className="flex flex-wrap gap-2">
          {plan.operations.map((operation, index) => (
            <Button
              asChild
              className={index === 0 ? "flex-1" : undefined}
              key={operation}
              size="lg"
              variant={index === 0 ? "default" : "outline"}
            >
              <Link href={`/account/store/${plan.planVersionId}?operation=${operation}`}>
                {translate(`operations.${OPERATIONS.includes(operation) ? operation : "unknown"}`)}
              </Link>
            </Button>
          ))}
        </div>
      ) : (
        // A refused plan keeps its price and its specification on screen and adds
        // the reason. Hiding it would leave the customer looking for the plan a
        // friend told them about and finding nothing at all.
        <p
          className="rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-[12.5px] leading-relaxed"
          role="note"
        >
          {explain(plan.ineligibleReason)}
        </p>
      )}
    </article>
  );
}
