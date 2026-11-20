"use client";

import { useLocale, useTranslations } from "next-intl";
import useSWR from "swr";

import { SectionLabel } from "@/components/account/state";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";

type Item = {
  contest: {
    id: string;
    titleRu: string;
    titleEn: string;
    endsAt: string;
    currency: string;
    prizes: number[];
  };
  standings: { place: number; referrals: number; you?: boolean }[];
};

/** Running referral contests: the prizes, the top places, and the customer's own place. */
export function ReferralContests() {
  const translate = useTranslations("account.account.contests");
  const locale = useLocale();
  const money = useMoney();
  const { data } = useSWR<{ items: Item[] }, ApiError>("/v1/account/referrals/contests", fetcher, {
    shouldRetryOnError: false,
  });
  const items = data?.items ?? [];
  if (items.length === 0) {
    return null;
  }
  return (
    <>
      <SectionLabel>{translate("title")}</SectionLabel>
      {items.map(({ contest, standings }) => (
        <section className="space-y-2 rounded-xl border border-border bg-card p-4" key={contest.id}>
          <p className="font-semibold text-[14px]">
            {locale === "ru" ? contest.titleRu : contest.titleEn}
          </p>
          <p className="text-[12.5px] text-muted-foreground">
            {translate("prizes", {
              prizes: contest.prizes.map((prize) => money(prize, contest.currency)).join(" · "),
            })}{" "}
            · {translate("until")} <Timestamp value={contest.endsAt} />
          </p>
          <ol className="text-[13px]">
            {standings.map((standing) => (
              <li className={standing.you ? "font-semibold" : undefined} key={standing.place}>
                {standing.place}. {standing.you ? translate("you") : translate("participant")} —{" "}
                {translate("referrals", { count: standing.referrals })}
              </li>
            ))}
          </ol>
        </section>
      ))}
    </>
  );
}
