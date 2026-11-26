"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import Link from "next/link";
import { useLocale, useTranslations } from "next-intl";
import { useCallback, useId, useState } from "react";

import { ownPeriodDays, periodSaving, SAVING_SHOWN } from "@/components/account/commerce/periods";
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

/** The named periods a number of days reads as, for the card's switcher. */
const NAMED = ["day", "week", "month", "quarter", "half_year", "year"];

/**
 * The name a number of days goes by, or `null` when it has none.
 *
 * A month is 28 to 31 days and a year 364 to 366 because operators write
 * either; anything else is honestly a number of days.
 */
export function periodNameOf(days: number): string | null {
  if (days === 1) return "day";
  if (days === 7) return "week";
  if (days >= 28 && days <= 31) return "month";
  if (days >= 89 && days <= 92) return "quarter";
  if (days >= 180 && days <= 184) return "half_year";
  if (days >= 364 && days <= 366) return "year";
  return null;
}

/**
 * Names a period of a plan for the switcher: as a segment ("Year") or as what
 * the headline price buys ("for a year"). Zero is the plan's own period, named
 * by its billing period when it has one and by its length otherwise.
 */
function usePeriodPhrase(form: "name" | "for"): (plan: PlanOffer, days: number) => string {
  const translate = useTranslations("account.commerce");
  return useCallback(
    (plan: PlanOffer, days: number) => {
      const length = days > 0 ? days : ownPeriodDays(plan.durationSeconds);
      const named =
        days === 0 && NAMED.includes(plan.billingPeriod)
          ? plan.billingPeriod
          : periodNameOf(length);
      return named
        ? translate(`switcher.${form}.${named}`)
        : translate(`switcher.${form}.days`, { count: length });
    },
    [form, translate],
  );
}

/**
 * The plan's measurable facts as a definition list.
 *
 * A definition list rather than a grid of divs so a screen reader reads "traffic,
 * 200 GB" as one pair instead of two loose strings. Each fact is a small tile,
 * label over value, so a long value such as "unlimited" wraps inside its own
 * tile instead of breaking the row beside it. The catalogue card leaves the
 * period out: its price line already says it. `periodDays` names one of the
 * plan's further periods in place of its own, as the plan page does when the
 * store card's switcher chose one.
 */
export function PlanSpecs({
  plan,
  periodDays = 0,
  withPeriod = true,
}: {
  plan: PlanOffer;
  periodDays?: number;
  withPeriod?: boolean;
}) {
  const translate = useTranslations("account.commerce");
  const formatBytes = useBytes();
  const period = usePeriodLabel();

  const rows: { label: string; value: string }[] = [
    ...(withPeriod
      ? [
          {
            label: translate("specs.period"),
            value:
              periodDays > 0
                ? translate("period.days", { count: periodDays })
                : period(plan.billingPeriod, plan.durationSeconds),
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
  const translatePicker = useTranslations("account.periodPicker");
  const locale = useLocale();
  const period = usePeriodLabel();
  const explain = useIneligibility();
  // Zero is the plan's own period, as in the checkout's `periodDays`.
  const [chosenDays, setChosenDays] = useState(0);

  const periods = plan.eligible ? (plan.periods ?? []) : [];
  const chosen = periods.find((choice) => choice.days === chosenDays);
  const price = chosen?.price ?? plan.price;
  const forPeriod = usePeriodPhrase("for");

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
            {formatMoney(price.amountMinor, price.currency, locale, {
              display: "narrowSymbol",
              trimZeroFraction: true,
            })}
          </span>
          <span className="text-[14px] text-muted-foreground">
            {periods.length > 0
              ? // Beside a switcher every period reads "for a year", "for a
                // month": "monthly" next to a one-off year would compare a
                // subscription with a purchase.
                forPeriod(plan, chosen?.days ?? 0)
              : period(plan.billingPeriod, plan.durationSeconds)}
          </span>
          {chosen?.highlighted && <Badge variant="warning">{translatePicker("best")}</Badge>}
        </p>
        {plan.description && <PlanDescription text={plan.description} />}
      </div>

      {periods.length > 0 && (
        <PeriodSwitcher chosenDays={chosenDays} onChange={setChosenDays} plan={plan} />
      )}

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
              <Link
                href={`/account/store/${plan.planVersionId}?operation=${operation}${
                  // The periods were priced for the first operation, so only
                  // its button carries the choice; another opens at the plan's
                  // own period and is chosen again at the checkout.
                  index === 0 && chosen ? `&period=${chosen.days}` : ""
                }`}
              >
                {plan.kind === "trial" && operation === "purchase"
                  ? translate("operations.activate")
                  : translate(
                      `operations.${OPERATIONS.includes(operation) ? operation : "unknown"}`,
                    )}
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

/**
 * A plan's description: a list when the operator wrote it as one line per
 * point, a paragraph otherwise.
 *
 * Plans are compared point by point — countries, speed, devices — and a
 * sentence with them run together had to be read rather than scanned.
 * A leading dash or bullet on a line is the author's own marker and is
 * dropped, so either way of writing a list renders the same.
 */
function PlanDescription({ text }: { text: string }) {
  const lines = text
    .split(/\r?\n/)
    .map((line) => line.replace(/^\s*(?:[-•*·]\s+)?/, "").trim())
    .filter(Boolean);
  if (lines.length < 2) {
    return <p className="text-[13.5px] text-muted-foreground leading-relaxed">{text.trim()}</p>;
  }
  return (
    <ul className="space-y-1 text-[13.5px] text-muted-foreground leading-relaxed">
      {lines.map((line) => (
        <li className="flex gap-2" key={line}>
          <span aria-hidden className="mt-[0.6em] size-1 shrink-0 rounded-full bg-primary" />
          <span className="min-w-0">{line}</span>
        </li>
      ))}
    </ul>
  );
}

/**
 * The periods a plan is sold for, as a segmented control: the plan's own
 * first, then each longer one with its saving per day.
 *
 * Native radios under the segments, so the group is one tab stop, arrows move
 * the choice, and a screen reader announces "year, minus 50 per cent, 2 of 3"
 * without any of it being rebuilt here. The prices behind the saving are the
 * server's; the card only reads them.
 */
function PeriodSwitcher({
  chosenDays,
  onChange,
  plan,
}: {
  chosenDays: number;
  onChange: (days: number) => void;
  plan: PlanOffer;
}) {
  const translate = useTranslations("account.commerce");
  const name = useId();
  const label = usePeriodPhrase("name");

  // In order of length, the plan's own among them: "week | month | year"
  // reads as a scale, and a week listed after the month did not.
  const ownDays = ownPeriodDays(plan.durationSeconds);
  const segments = [
    { days: 0, label: label(plan, 0), length: ownDays, saving: 0 },
    ...(plan.periods ?? []).map((choice) => ({
      days: choice.days,
      label: label(plan, choice.days),
      length: choice.days,
      saving: periodSaving(plan, choice.days, choice.price),
    })),
  ].sort((left, right) => left.length - right.length);

  return (
    <fieldset>
      <legend className="sr-only">{translate("switcher.label")}</legend>
      <div className="grid grid-cols-[repeat(auto-fit,minmax(4.5rem,1fr))] gap-1 rounded-lg bg-well p-1">
        {segments.map((segment) => (
          <label
            className={cn(
              "flex min-h-11 cursor-pointer flex-col items-center justify-center rounded-md px-1.5 py-1 text-center transition-colors",
              "text-muted-foreground hover:text-foreground",
              "has-[:checked]:bg-card has-[:checked]:text-foreground has-[:checked]:shadow-sm",
              "has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-ring has-[:focus-visible]:outline-offset-1",
            )}
            key={segment.days}
          >
            <input
              checked={chosenDays === segment.days}
              className="sr-only"
              name={name}
              onChange={() => onChange(segment.days)}
              type="radio"
              value={segment.days}
            />
            <span className="font-semibold text-[13px] leading-tight">{segment.label}</span>
            {segment.saving >= SAVING_SHOWN && (
              <span className="font-semibold text-[11.5px] text-success leading-tight" data-numeric>
                −{segment.saving}%
              </span>
            )}
          </label>
        ))}
      </div>
    </fieldset>
  );
}
