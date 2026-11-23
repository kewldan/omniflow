"use client";

import { useFormatter, useTranslations } from "next-intl";
import useSWR from "swr";

import { SectionLabel } from "@/components/account/state";
import { type ApiError, fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";

export type ReferralInsightsData = {
  rewardChoice: { allowed: boolean; kind: "" | "wallet" | "days" };
  earnings: { last7Minor: number; last30Minor: number; allMinor: number };
  invitees: { joinedAt: string; paid: boolean; earnedMinor: number }[];
};

/**
 * The customer's reward choice, earnings by period, and invitees.
 *
 * One read shared by the sections that show its parts — the earnings beside the
 * total, the choice beside the payout rules, the invitees in their own list —
 * so SWR deduplicates it into a single request.
 */
export function useReferralInsights() {
  return useSWR<ReferralInsightsData, ApiError>("/v1/account/referrals/insights", fetcher);
}

/**
 * The friends invited: when each joined, whether they paid, and what they
 * earned the customer. Nothing on it identifies a friend.
 */
export function ReferralInvitees({ currency }: { currency: string }) {
  const translate = useTranslations("account.account.referralInsights");
  const money = useMoney();
  const format = useFormatter();
  const { data } = useReferralInsights();

  if (!data) {
    return null;
  }

  return (
    <>
      <SectionLabel>{translate("invitees")}</SectionLabel>
      {data.invitees.length === 0 ? (
        <p className="px-1 text-[12.5px] text-muted-foreground">{translate("noInvitees")}</p>
      ) : (
        <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
          {data.invitees.map((invitee) => (
            <li
              className="flex items-center justify-between gap-3 px-4 py-2.5 text-[13px]"
              key={invitee.joinedAt}
            >
              <span>
                {format.dateTime(new Date(invitee.joinedAt), {
                  day: "numeric",
                  month: "short",
                  year: "numeric",
                })}
              </span>
              <span className="text-muted-foreground">
                {invitee.paid ? translate("paid") : translate("notPaid")}
              </span>
              <span data-numeric>{money(invitee.earnedMinor, currency)}</span>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}
