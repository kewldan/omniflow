"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { useFormatter, useTranslations } from "next-intl";
import { useState } from "react";

import { useProblemMessage } from "@/components/account/account/problem";
import type {
  ReferralReward,
  ReferralRewardPage,
  ReferralSummary,
  RewardState,
} from "@/components/account/account/types";
import { AccountNotice } from "@/components/account/state";
import { apiFetch, toQuery } from "@/lib/api";
import { useMoney } from "@/lib/format";

/** Each state's tone, kept in one place so the badge and the amount never disagree. */
const STATE_TONE: Record<RewardState, "danger" | "success" | "warning"> = {
  pending: "warning",
  qualified: "success",
  rejected: "danger",
};

/** How many rewards a "load more" adds. Two screens' worth on a phone. */
const PAGE_SIZE = 20;

/**
 * The reward history, page by page.
 *
 * The first page arrives inside the referral summary, so the screen renders in
 * one request; further pages come from the same route with a cursor. Nothing is
 * fetched until the customer asks for it — most people have a handful of
 * rewards and would pay for a second request they never look at.
 */
export function RewardHistory({ firstPage }: { firstPage: ReferralRewardPage }) {
  const translate = useTranslations("account.account");
  const format = useFormatter();
  const money = useMoney();
  const describeProblem = useProblemMessage();

  const [extra, setExtra] = useState<ReferralReward[]>([]);
  const [cursor, setCursor] = useState(firstPage.nextCursor);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  /*
   * The first page is whatever the summary last returned and the extra pages are
   * what this component fetched, so a background revalidation refreshes the top
   * of the list without discarding what the customer already scrolled to. The
   * two can overlap when a reward is granted between requests, which is why they
   * are merged by identity rather than concatenated.
   */
  const seen = new Set<string>();
  const rewards: ReferralReward[] = [];
  for (const reward of [...firstPage.items, ...extra]) {
    if (!seen.has(reward.id)) {
      seen.add(reward.id);
      rewards.push(reward);
    }
  }

  async function loadMore() {
    setBusy(true);
    setFailure(null);
    try {
      const next = await apiFetch<ReferralSummary>(
        `/v1/account/referrals${toQuery({ cursor, limit: PAGE_SIZE })}`,
      );
      setExtra((current) => [...current, ...next.rewards.items]);
      setCursor(next.rewards.nextCursor);
    } catch (loadError) {
      // Inline rather than a toast: the failure belongs to the button that was
      // pressed, and the customer needs it still there to press again.
      setFailure(describeProblem(loadError));
    } finally {
      setBusy(false);
    }
  }

  if (rewards.length === 0) {
    return (
      <AccountNotice
        description={translate("referrals.historyEmptyDescription")}
        title={translate("referrals.historyEmpty")}
      />
    );
  }

  return (
    <div className="space-y-3">
      <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
        {rewards.map((reward) => (
          <li className="flex items-start gap-3 px-4 py-3.5" key={reward.id}>
            <div className="min-w-0 flex-1">
              <p className="font-medium text-[14px]">
                {translate(`referrals.role.${reward.role === "invitee" ? "invitee" : "inviter"}`)}
              </p>
              <p className="mt-0.5 font-mono text-[11px] text-subtle-foreground">
                {format.dateTime(new Date(reward.grantedAt), {
                  day: "numeric",
                  month: "short",
                  year: "numeric",
                })}
              </p>
              {reward.reversedAt && (
                <p className="mt-0.5 font-mono text-[11px] text-destructive">
                  {translate("referrals.reversedOn", {
                    date: format.dateTime(new Date(reward.reversedAt), {
                      day: "numeric",
                      month: "short",
                      year: "numeric",
                    }),
                  })}
                </p>
              )}
            </div>
            <div className="shrink-0 space-y-1 text-right">
              <p className="font-semibold text-[14px]" data-numeric>
                {money(reward.amountMinor, reward.currency)}
              </p>
              <Badge variant={STATE_TONE[reward.state]}>
                {translate(`referrals.state.${reward.state}`)}
              </Badge>
            </div>
          </li>
        ))}
      </ul>

      {failure && (
        <p className="px-1 text-[12.5px] text-destructive leading-relaxed" role="alert">
          {failure}
        </p>
      )}

      {cursor && (
        <Button className="w-full" disabled={busy} onClick={loadMore} size="lg" variant="outline">
          {busy ? translate("actions.loading") : translate("actions.loadMore")}
        </Button>
      )}
    </div>
  );
}
