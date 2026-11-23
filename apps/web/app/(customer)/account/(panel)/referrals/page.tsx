"use client";

import { useTranslations } from "next-intl";
import type { ReactNode } from "react";
import useSWR from "swr";
import { DailyGames } from "@/components/account/account/daily-games";
import { Engagement } from "@/components/account/account/engagement";
import { LoyaltyStanding } from "@/components/account/account/loyalty-standing";
import {
  inviteeReward,
  inviterReward,
  isRewarded,
  PayoutTerms,
  useRewardLabel,
} from "@/components/account/account/payout-terms";
import { ReferralContests } from "@/components/account/account/referral-contests";
import {
  ReferralInvitees,
  useReferralInsights,
} from "@/components/account/account/referral-insights";
import { ReferralSwitch } from "@/components/account/account/referral-switch";
import { RewardHistory } from "@/components/account/account/reward-history";
import { ShareLink } from "@/components/account/account/share-link";
import type { ReferralSummary } from "@/components/account/account/types";
import { Withdrawals } from "@/components/account/account/withdrawals";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import type { ApiError } from "@/lib/api";
import { fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";

/**
 * Inviting a friend. The partner programme is its own place at
 * `/account/referrals/partner`, reached from the switch at the top of both.
 *
 * Above the fold there is one card: the offer in a sentence, the code, the link
 * and its QR. A customer opening this screen for the tenth time wants their
 * link, not a restatement of the offer. Below it, in the order somebody
 * scrolling needs them: what invites have earned, how that is paid out, the
 * withdrawal form, the friends, the history. Contests, games, and loyalty load
 * independently underneath, so an installation running some and not others
 * still gets a coherent page.
 */
export default function ReferralsPage() {
  const translate = useTranslations("account.account");
  const money = useMoney();
  const { data, error, isLoading } = useSWR<ReferralSummary, ApiError>(
    "/v1/account/referrals",
    fetcher,
  );
  const { data: insights } = useReferralInsights();
  const label = useRewardLabel(data?.program.currency ?? "");

  let offer = "";
  if (data?.program.enabled) {
    const mine = inviterReward(data.program, insights?.rewardChoice);
    const friend = inviteeReward(data.program);
    if (isRewarded(mine) && isRewarded(friend)) {
      offer = translate("referrals.offer.both", { friend: label(friend), you: label(mine) });
    } else if (isRewarded(mine)) {
      offer = translate("referrals.offer.inviter", { you: label(mine) });
    } else if (isRewarded(friend)) {
      offer = translate("referrals.offer.invitee", { friend: label(friend) });
    }
  }

  return (
    <div className="animate-step-in space-y-5">
      <ReferralSwitch />

      {isLoading && <ListSkeleton rows={2} />}

      {error && (
        <AccountNotice
          description={translate("states.loadErrorDescription")}
          title={translate("states.loadError")}
          variant="danger"
        />
      )}

      {data && !data.program.enabled && (
        <AccountNotice
          description={translate("referrals.disabledDescription")}
          title={translate("referrals.disabled")}
        />
      )}

      {data?.program.enabled && (
        <>
          <ShareLink
            code={data.code}
            link={data.link}
            linkAvailable={data.linkAvailable}
            linkReason={data.linkReason}
            offer={offer}
            shareText={
              isRewarded(inviteeReward(data.program))
                ? translate("referrals.shareText", { reward: label(inviteeReward(data.program)) })
                : translate("referrals.shareTextPlain")
            }
            shareTitle={translate("referrals.shareTitle")}
          />

          <SectionLabel>{translate("referrals.yourRewards")}</SectionLabel>
          <section className="space-y-3 rounded-xl border border-border bg-card p-4">
            <div className="flex items-center justify-between gap-3">
              <span className="text-[14px] text-muted-foreground">
                {translate("referrals.earned")}
              </span>
              <span className="font-bold text-[26px] leading-none tracking-[-0.04em]" data-numeric>
                {money(data.rewardedMinor, data.currency)}
              </span>
            </div>
            <dl className="space-y-1.5 border-border border-t pt-3">
              {insights && (
                <>
                  <Figure label={translate("referralInsights.periods.last7")}>
                    {money(insights.earnings.last7Minor, data.currency)}
                  </Figure>
                  <Figure label={translate("referralInsights.periods.last30")}>
                    {money(insights.earnings.last30Minor, data.currency)}
                  </Figure>
                </>
              )}
              <Figure label={translate("referrals.invited")}>{data.invited}</Figure>
              <Figure label={translate("referrals.qualified")}>{data.qualified}</Figure>
              {data.pending > 0 && (
                <Figure label={translate("referrals.pending")}>{data.pending}</Figure>
              )}
              {data.rejected > 0 && (
                <Figure label={translate("referrals.rejected")}>{data.rejected}</Figure>
              )}
            </dl>
            {/* A total that went down needs the reason beside it, or the
                customer is left comparing it against a number they remember. */}
            {data.reversedMinor > 0 && (
              <p className="text-[12px] text-muted-foreground leading-relaxed">
                {translate("referrals.reversedTotal", {
                  amount: money(data.reversedMinor, data.currency),
                })}
              </p>
            )}
            {data.remainingSlots !== null && (
              <p className="text-[12px] text-muted-foreground leading-relaxed">
                {data.remainingSlots > 0
                  ? translate("referrals.slotsLeft", {
                      cap: data.program.inviterRewardCap ?? 0,
                      remaining: data.remainingSlots,
                    })
                  : translate("referrals.slotsSpent", {
                      cap: data.program.inviterRewardCap ?? 0,
                    })}
              </p>
            )}
          </section>

          <PayoutTerms program={data.program} />

          <Withdrawals />

          <ReferralInvitees currency={data.currency} />

          <SectionLabel>{translate("referrals.history")}</SectionLabel>
          <RewardHistory firstPage={data.rewards} />
        </>
      )}

      <ReferralContests />

      <Engagement />

      <DailyGames />

      <SectionLabel>{translate("loyalty.title")}</SectionLabel>
      <LoyaltyStanding />
    </div>
  );
}

/** One secondary figure under the headline total. */
function Figure({ children, label }: { children: ReactNode; label: string }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="text-[12.5px] text-muted-foreground">{label}</dt>
      <dd className="font-medium text-[13px]" data-numeric>
        {children}
      </dd>
    </div>
  );
}
