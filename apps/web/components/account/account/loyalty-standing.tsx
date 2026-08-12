"use client";

import { Badge } from "@omniflow/ui/badge";
import { cn } from "@omniflow/ui/lib/utils";
import { useFormatter, useLocale, useTranslations } from "next-intl";
import useSWR from "swr";

import type {
  LoyaltyMetric,
  LoyaltyStanding as LoyaltyStandingResponse,
  LoyaltyTier,
} from "@/components/account/account/types";
import { AccountNotice, ListSkeleton } from "@/components/account/state";
import type { ApiError } from "@/lib/api";
import { fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";

/**
 * The customer's standing on the loyalty ladder.
 *
 * It loads itself rather than taking data from the referral screen around it,
 * because the two programmes are configured independently: an installation can
 * run invites with no tiers, or tiers with no invites, and neither screen should
 * fail because the other's route did.
 */
export function LoyaltyStanding() {
  const translate = useTranslations("account.account");
  const format = useFormatter();
  const locale = useLocale();
  const money = useMoney();
  const { data, error, isLoading } = useSWR<LoyaltyStandingResponse, ApiError>(
    "/v1/account/loyalty",
    fetcher,
  );

  /** The tier's name in the language being read, not always the English one. */
  function tierName(tier: LoyaltyTier): string {
    return (locale === "ru" ? tier.nameRu : tier.nameEn) || tier.code;
  }

  /**
   * The metric in its own unit.
   *
   * Spend is money and has to go through the currency's exponent; tenure is a
   * count of days and orders is a count of orders. Formatting all three as a
   * bare number would quietly price a tier threshold a hundred times too low.
   */
  function metricValue(value: number, metric: LoyaltyMetric, currency: string): string {
    if (metric === "spend") {
      return money(value, currency);
    }
    return translate(metric === "tenure" ? "loyalty.days" : "loyalty.orders", { count: value });
  }

  /** Basis points as the percentage a person reads. */
  function discount(bps: number): string {
    return format.number(bps / 10_000, { maximumFractionDigits: 2, style: "percent" });
  }

  if (isLoading) {
    return <ListSkeleton rows={2} />;
  }
  if (error) {
    return (
      <AccountNotice
        description={translate("states.loadErrorDescription")}
        title={translate("states.loadError")}
        variant="danger"
      />
    );
  }
  if (!data?.enabled) {
    return (
      <AccountNotice
        description={translate("loyalty.disabledDescription")}
        title={translate("loyalty.disabled")}
      />
    );
  }

  const { rules, tiers } = data;
  const currentTier = data.evaluated ? data.tier : undefined;

  return (
    <div className="space-y-4">
      {currentTier ? (
        <section className="space-y-3 rounded-xl border border-border bg-card p-4">
          <div className="flex items-baseline justify-between gap-3">
            <div className="min-w-0">
              <p className="font-mono text-[11px] text-subtle-foreground">
                {translate("loyalty.currentTier")}
              </p>
              <p className="mt-1 truncate font-semibold text-[19px] tracking-[-0.01em]">
                {tierName(currentTier)}
              </p>
            </div>
            {currentTier.discountBps > 0 && (
              <Badge variant="success">
                {translate("loyalty.discount", { percent: discount(currentTier.discountBps) })}
              </Badge>
            )}
          </div>

          <p className="font-mono text-[11px] text-muted-foreground">
            {translate("loyalty.measured", {
              value: metricValue(data.metric ?? 0, rules.metric, rules.currency),
            })}
          </p>

          {data.next ? (
            <TierProgress
              label={translate("loyalty.toNext", {
                remaining: metricValue(data.remaining ?? 0, rules.metric, rules.currency),
                tier: tierName(data.next),
              })}
              percent={data.percent ?? 0}
            />
          ) : (
            <p className="text-[12.5px] text-muted-foreground leading-relaxed">
              {translate("loyalty.topTier")}
            </p>
          )}

          {/* A tier held through grace is not a tier the customer currently
              earns, and saying so is the difference between a pleasant surprise
              and an unpleasant one at the end of the window. */}
          {data.graceUntil && (
            <p
              className="rounded-lg border border-warning/40 bg-warning/10 px-3 py-2.5 text-[12.5px] leading-relaxed"
              role="status"
            >
              {translate("loyalty.grace", {
                date: format.dateTime(new Date(data.graceUntil), {
                  day: "numeric",
                  month: "long",
                  year: "numeric",
                }),
              })}
            </p>
          )}

          {data.evaluatedAt && (
            <p className="font-mono text-[11px] text-subtle-foreground">
              {translate("loyalty.evaluatedAt", {
                date: format.dateTime(new Date(data.evaluatedAt), {
                  day: "numeric",
                  month: "short",
                  year: "numeric",
                }),
              })}
            </p>
          )}
        </section>
      ) : (
        // Enabled, but nobody has been placed yet. Computing a tier here would be
        // a promotion decided by whoever opened a page, so the ladder is shown
        // and the placement is honestly described as pending.
        <AccountNotice
          description={translate("loyalty.pendingDescription")}
          title={translate("loyalty.pending")}
        />
      )}

      <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
        {tiers.map((tier) => (
          <li
            className={cn("flex items-center gap-3 px-4 py-3", tier.current && "bg-accent/60")}
            key={tier.code}
          >
            <span className="min-w-0 flex-1">
              <span className="block truncate font-medium text-[14px]">{tierName(tier)}</span>
              <span className="mt-0.5 block font-mono text-[11px] text-subtle-foreground">
                {translate("loyalty.threshold", {
                  value: metricValue(tier.threshold, rules.metric, rules.currency),
                })}
              </span>
            </span>
            {tier.discountBps > 0 && (
              <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
                {discount(tier.discountBps)}
              </span>
            )}
            {tier.current && <Badge variant="solid">{translate("loyalty.you")}</Badge>}
          </li>
        ))}
      </ul>

      <div className="space-y-1.5 rounded-xl border border-border bg-card p-4">
        <p className="font-medium text-[13.5px]">{translate("loyalty.rules")}</p>
        <p className="text-[12.5px] text-muted-foreground leading-relaxed">
          {translate(`loyalty.metricRule.${rules.metric}`, { days: rules.windowDays })}
        </p>
        <p className="text-[12.5px] text-muted-foreground leading-relaxed">
          {rules.graceDays > 0
            ? translate("loyalty.graceRule", { days: rules.graceDays })
            : translate("loyalty.noGraceRule")}
        </p>
      </div>
    </div>
  );
}

/**
 * The bar and the sentence that says the same thing.
 *
 * The sentence is the visible label rather than a hidden one, and the bar
 * carries it as its accessible name: a progressbar announcing a bare percentage
 * tells a screen-reader user nothing about what is being approached.
 */
function TierProgress({ label, percent }: { label: string; percent: number }) {
  const clamped = Math.min(100, Math.max(0, percent));
  return (
    <div className="space-y-2">
      <p className="font-mono text-[11px] text-muted-foreground">{label}</p>
      <div aria-label={label} className="h-1 overflow-hidden rounded-full bg-muted" role="img">
        <div
          className="h-full rounded-full bg-primary transition-[width] duration-700 ease-emphasis motion-reduce:transition-none"
          style={{ width: `${clamped}%` }}
        />
      </div>
    </div>
  );
}
