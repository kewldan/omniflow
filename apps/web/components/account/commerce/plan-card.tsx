"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import Link from "next/link";
import { useLocale, useTranslations } from "next-intl";
import { useCallback } from "react";

import { useIneligibility } from "@/components/account/commerce/reasons";
import type { PlanOffer } from "@/components/account/commerce/types";
import { useBytes } from "@/lib/format";
import { formatMoney } from "@/lib/money";

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
 * 200 GB" as one pair instead of two loose strings. Each fact is a small tile,
 * label over value, so a long value such as "unlimited" wraps inside its own
 * tile instead of breaking the row beside it. The catalogue card leaves the
 * period out: its price line already says it.
 */
export function PlanSpecs({ plan, withPeriod = true }: { plan: PlanOffer; withPeriod?: boolean }) {
  const translate = useTranslations("account.commerce");
  const formatBytes = useBytes();
  const period = usePeriodLabel();

  const rows: { label: string; value: string }[] = [
    ...(withPeriod
      ? [
          {
            label: translate("specs.period"),
            value: period(plan.billingPeriod, plan.durationSeconds),
          },
        ]
      : []),
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
    <dl className="grid grid-cols-2 gap-2">
      {rows.map((row) => (
        <div className="min-w-0 rounded-lg bg-well px-3 py-2.5" key={row.label}>
          <dt className="text-[12px] text-muted-foreground">{row.label}</dt>
          <dd className="mt-0.5 font-semibold text-[14px] leading-snug" data-numeric>
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
  const locale = useLocale();
  const period = usePeriodLabel();
  const explain = useIneligibility();

  return (
    <article
      className={cn(
        "animate-step-in space-y-4 rounded-xl border border-border bg-card p-5",
        // The plan the customer is on is the one they are comparing everything
        // else against, so it is marked rather than left to be inferred from a
        // button label.
        plan.held && "border-primary/50 ring-1 ring-primary/20",
        !plan.eligible && "opacity-80",
      )}
    >
      {/* Name and badges, then the price as the headline: it is the figure a
          customer compares plans on, and it used to sit in a corner beside a
          paragraph of description. */}
      <div className="space-y-1.5">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5">
          <h3 className="font-semibold text-[17px] tracking-[-0.01em]">{plan.name}</h3>
          {plan.held && <Badge variant="success">{translate("kind.current")}</Badge>}
          {plan.highlighted && <Badge variant="warning">{translate("kind.highlighted")}</Badge>}
          {plan.kind === "trial" && <Badge variant="info">{translate("kind.trial")}</Badge>}
        </div>
        <p className="flex flex-wrap items-baseline gap-x-2">
          <span className="font-bold text-[30px] leading-tight tracking-[-0.03em]" data-numeric>
            {formatMoney(plan.price.amountMinor, plan.price.currency, locale, {
              display: "narrowSymbol",
              trimZeroFraction: true,
            })}
          </span>
          <span className="text-[14px] text-muted-foreground">
            {period(plan.billingPeriod, plan.durationSeconds)}
          </span>
        </p>
        {plan.description && (
          <p className="text-[13.5px] text-muted-foreground leading-relaxed">{plan.description}</p>
        )}
      </div>

      {(plan.recurringCapable || plan.configurableSquads) && (
        <div className="flex flex-wrap gap-1.5">
          {plan.recurringCapable && <Badge variant="outline">{translate("kind.recurring")}</Badge>}
          {plan.configurableSquads && <Badge variant="outline">{translate("kind.squads")}</Badge>}
        </div>
      )}

      <PlanSpecs plan={plan} withPeriod={false} />

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
