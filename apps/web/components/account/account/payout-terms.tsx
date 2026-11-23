"use client";

import { Button } from "@omniflow/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { ExternalLink } from "lucide-react";
import { useTranslations } from "next-intl";
import { type ReactNode, useId } from "react";

import {
  type ReferralInsightsData,
  useReferralInsights,
} from "@/components/account/account/referral-insights";
import type { ReferralProgram } from "@/components/account/account/types";
import { useWithdrawalSummary } from "@/components/account/account/withdrawals";
import { SectionLabel } from "@/components/account/state";
import { apiFetch } from "@/lib/api";
import { useMoney } from "@/lib/format";

/** One side's reward: days of subscription, or money credited to the wallet. */
export type SideReward = { days: number; minor: number };

/**
 * What the inviter is actually paid, by the rule the reward worker applies
 * (`internal/commercepg/referral.go`): a day reward replaces the money reward,
 * unless the programme lets the customer choose and they chose the balance.
 */
export function inviterReward(
  program: ReferralProgram,
  choice?: ReferralInsightsData["rewardChoice"],
): SideReward {
  const days = program.inviterRewardDays ?? 0;
  const wallet = choice?.allowed && choice.kind === "wallet" && program.inviterRewardMinor > 0;
  return days > 0 && !wallet ? { days, minor: 0 } : { days: 0, minor: program.inviterRewardMinor };
}

/** The friend's reward. Their own choice is theirs, so days win as for anyone who has not picked. */
export function inviteeReward(program: ReferralProgram): SideReward {
  const days = program.inviteeRewardDays ?? 0;
  return days > 0 ? { days, minor: 0 } : { days: 0, minor: program.inviteeRewardMinor };
}

export function isRewarded(reward: SideReward): boolean {
  return reward.days > 0 || reward.minor > 0;
}

/** A reward as words: "7 days of subscription" or "100 ₽". */
export function useRewardLabel(currency: string) {
  const translate = useTranslations("account.account.referrals");
  const money = useMoney();
  return (reward: SideReward) =>
    reward.days > 0 ? translate("days", { count: reward.days }) : money(reward.minor, currency);
}

/**
 * Whether money credited to the wallet can be taken out, from the operator's
 * withdrawal policy. A 404 is an installation without withdrawals at all; any
 * other failure leaves the answer unknown, and nothing is promised either way.
 */
export function useWithdrawability():
  | {
      state: "yes";
      minimumMinor: number;
      cooldownDays: number;
      currency: string;
    }
  | { state: "no" | "unknown" } {
  const { data, error } = useWithdrawalSummary();
  if (data) {
    return data.enabled
      ? {
          cooldownDays: data.cooldownDays,
          currency: data.currency,
          minimumMinor: data.minimumMinor,
          state: "yes",
        }
      : { state: "no" };
  }
  return { state: error?.status === 404 ? "no" : "unknown" };
}

/**
 * How an invite reward is paid, said plainly and only from configuration:
 * where it lands, whether it can be withdrawn and from how much, when it is
 * granted, and when unspent money expires. Every sentence here is a rule the
 * server enforces; none is a paraphrase of what an operator might intend.
 */
export function PayoutTerms({ program }: { program: ReferralProgram }) {
  const translate = useTranslations("account.account.referrals");
  const money = useMoney();
  const label = useRewardLabel(program.currency);
  const choiceId = useId();
  const { data: insights, mutate } = useReferralInsights();
  const withdrawable = useWithdrawability();

  const mine = inviterReward(program, insights?.rewardChoice);
  const friend = inviteeReward(program);
  const toWallet = mine.days === 0 && mine.minor > 0;

  async function choose(kind: string) {
    try {
      await apiFetch("/v1/account/referrals/reward-kind", {
        body: JSON.stringify({ kind }),
        method: "PUT",
      });
    } finally {
      void mutate();
    }
  }

  let paidTo = "";
  if (mine.days > 0) {
    paidTo = translate("payout.days");
  } else if (toWallet) {
    paidTo = translate(
      withdrawable.state === "yes"
        ? "payout.walletWithdrawable"
        : withdrawable.state === "no"
          ? "payout.walletOnly"
          : "payout.wallet",
    );
  }

  return (
    <>
      <SectionLabel>{translate("payout.title")}</SectionLabel>
      <section className="space-y-3 rounded-xl border border-border bg-card p-4">
        {paidTo && <p className="text-[13.5px] leading-relaxed">{paidTo}</p>}

        {insights?.rewardChoice.allowed && (
          <div className="flex items-center justify-between gap-3">
            <label className="text-[13px] text-muted-foreground" htmlFor={choiceId}>
              {translate("payout.choice")}
            </label>
            <Select
              onValueChange={(kind) => void choose(kind)}
              value={insights.rewardChoice.kind || "days"}
            >
              <SelectTrigger className="w-auto min-w-36" id={choiceId}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="days">{translate("payout.kinds.days")}</SelectItem>
                <SelectItem value="wallet">{translate("payout.kinds.wallet")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
        )}

        <dl className="space-y-2 border-border border-t pt-3">
          {isRewarded(mine) && <Row label={translate("inviterReward")}>{label(mine)}</Row>}
          {isRewarded(friend) && <Row label={translate("inviteeReward")}>{label(friend)}</Row>}
          {toWallet && withdrawable.state === "yes" && (
            <Row label={translate("payout.minimum")}>
              {money(withdrawable.minimumMinor, withdrawable.currency)}
            </Row>
          )}
          {toWallet && withdrawable.state === "yes" && withdrawable.cooldownDays > 0 && (
            <Row label={translate("payout.cooldown")}>
              {translate("payout.everyDays", { count: withdrawable.cooldownDays })}
            </Row>
          )}
          {toWallet && program.rewardExpiryDays !== null && (
            <Row label={translate("payout.expires")}>
              {translate("payout.afterDays", { count: program.rewardExpiryDays })}
            </Row>
          )}
        </dl>

        <p className="text-[12.5px] text-muted-foreground leading-relaxed">
          {translate(`qualification.${program.qualification}`)}{" "}
          {translate("attribution", { days: program.attributionValidityDays })}
        </p>

        {/* The terms live on the operator's own page, so the link leaves the
            panel and the label says so rather than surprising anybody. The
            URL is checked before it is rendered: it is operator-supplied
            text, and an href is the one place where a `javascript:` string
            would be executed rather than displayed. */}
        {externalHref(program.termsUrl) && (
          <Button asChild className="w-full" size="lg" variant="outline">
            <a href={program.termsUrl} rel="noreferrer noopener" target="_blank">
              <ExternalLink aria-hidden />
              {translate("readTerms")}
              <span className="sr-only"> {translate("newTab")}</span>
            </a>
          </Button>
        )}
      </section>
    </>
  );
}

/** True when a configured URL is one a browser may safely navigate to. */
function externalHref(url: string | undefined): boolean {
  if (!url) {
    return false;
  }
  try {
    const parsed = new URL(url);
    return parsed.protocol === "https:" || parsed.protocol === "http:";
  } catch {
    return false;
  }
}

/** A labelled figure: the label on the left, the value on the right. */
function Row({ children, label }: { children: ReactNode; label: string }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="text-[13px] text-muted-foreground">{label}</dt>
      <dd className="shrink-0 text-end font-semibold text-[13.5px]" data-numeric>
        {children}
      </dd>
    </div>
  );
}
