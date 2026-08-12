"use client";

import { Button } from "@omniflow/ui/button";
import { ExternalLink } from "lucide-react";
import { useTranslations } from "next-intl";
import type { ReactNode } from "react";
import useSWR from "swr";

import { LoyaltyStanding } from "@/components/account/account/loyalty-standing";
import { RewardHistory } from "@/components/account/account/reward-history";
import { ShareLink } from "@/components/account/account/share-link";
import type { ReferralSummary } from "@/components/account/account/types";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import type { ApiError } from "@/lib/api";
import { fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";

/**
 * The invites screen.
 *
 * Two programmes share it because they answer the same question — what does
 * this account earn me — and because neither is large enough to justify a tab of
 * its own in a five-tab bar. Loyalty loads independently underneath, so an
 * installation running one and not the other still gets a coherent page.
 *
 * The order is the order a customer needs it in: the code they came to fetch,
 * then what it has earned, then the rules behind those numbers, then the
 * history. The terms sit below the counts rather than above them because
 * somebody opening this screen for the tenth time wants their code, not a
 * restatement of the offer.
 */
export default function ReferralsPage() {
  const translate = useTranslations("account.account");
  const money = useMoney();
  const { data, error, isLoading } = useSWR<ReferralSummary, ApiError>(
    "/v1/account/referrals",
    fetcher,
  );

  return (
    <div className="animate-step-in space-y-5">
      <SectionLabel>{translate("referrals.title")}</SectionLabel>

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
            shareText={translate("referrals.shareText", {
              reward: money(data.program.inviteeRewardMinor, data.program.currency),
            })}
            shareTitle={translate("referrals.shareTitle")}
          />

          <section className="rounded-xl border border-border bg-card p-4">
            <p className="font-mono text-[11px] text-subtle-foreground">
              {translate("referrals.earned")}
            </p>
            <p className="mt-1 font-bold text-[30px] leading-none tracking-[-0.04em]" data-numeric>
              {money(data.rewardedMinor, data.currency)}
            </p>
            {/* A total that went down needs the reason beside it, or the
                customer is left comparing it against a number they remember. */}
            {data.reversedMinor > 0 && (
              <p className="mt-2 text-[12.5px] text-muted-foreground leading-relaxed">
                {translate("referrals.reversedTotal", {
                  amount: money(data.reversedMinor, data.currency),
                })}
              </p>
            )}
            {data.remainingSlots !== null && (
              <p className="mt-2 text-[12.5px] text-muted-foreground leading-relaxed">
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

          <div className="grid grid-cols-2 gap-3">
            <Stat label={translate("referrals.invited")} value={data.invited} />
            <Stat label={translate("referrals.qualified")} value={data.qualified} />
            <Stat label={translate("referrals.pending")} value={data.pending} />
            <Stat label={translate("referrals.rejected")} value={data.rejected} />
          </div>

          <SectionLabel>{translate("referrals.terms")}</SectionLabel>
          <section className="space-y-2 rounded-xl border border-border bg-card p-4">
            <Term label={translate("referrals.inviterReward")}>
              {money(data.program.inviterRewardMinor, data.program.currency)}
            </Term>
            <Term label={translate("referrals.inviteeReward")}>
              {money(data.program.inviteeRewardMinor, data.program.currency)}
            </Term>
            <p className="text-[12.5px] text-muted-foreground leading-relaxed">
              {translate(`referrals.qualification.${data.program.qualification}`)}
            </p>
            <p className="text-[12.5px] text-muted-foreground leading-relaxed">
              {translate("referrals.attribution", {
                days: data.program.attributionValidityDays,
              })}
            </p>
            {data.program.rewardExpiryDays !== null && (
              <p className="text-[12.5px] text-muted-foreground leading-relaxed">
                {translate("referrals.rewardExpiry", { days: data.program.rewardExpiryDays })}
              </p>
            )}
            {/* The terms live on the operator's own page, so the link leaves the
                panel and the label says so rather than surprising anybody. The
                URL is checked before it is rendered: it is operator-supplied
                text, and an href is the one place where a `javascript:` string
                would be executed rather than displayed. */}
            {externalHref(data.program.termsUrl) && (
              <Button asChild className="w-full" size="lg" variant="outline">
                <a href={data.program.termsUrl} rel="noreferrer noopener" target="_blank">
                  <ExternalLink aria-hidden />
                  {translate("referrals.readTerms")}
                </a>
              </Button>
            )}
          </section>

          <SectionLabel>{translate("referrals.history")}</SectionLabel>
          <RewardHistory firstPage={data.rewards} />
        </>
      )}

      <SectionLabel>{translate("loyalty.title")}</SectionLabel>
      <LoyaltyStanding />
    </div>
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

/** One count, sized so four of them read as a block rather than a sentence. */
function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-xl border border-border bg-card p-4">
      <p className="font-bold text-[26px] leading-none tracking-[-0.04em]" data-numeric>
        {value}
      </p>
      <p className="mt-1.5 font-mono text-[11px] text-subtle-foreground">{label}</p>
    </div>
  );
}

/** A labelled figure in the terms card. */
function Term({ children, label }: { children: ReactNode; label: string }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <span className="text-[13px] text-muted-foreground">{label}</span>
      <span className="shrink-0 font-semibold text-[14px]" data-numeric>
        {children}
      </span>
    </div>
  );
}
