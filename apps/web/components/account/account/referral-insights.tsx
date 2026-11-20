"use client";

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { useFormatter, useTranslations } from "next-intl";
import { useId } from "react";
import useSWR from "swr";

import { SectionLabel } from "@/components/account/state";
import { QrCode } from "@/components/qr-code";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";

type Insights = {
  rewardChoice: { allowed: boolean; kind: "" | "wallet" | "days" };
  earnings: { last7Minor: number; last30Minor: number; allMinor: number };
  invitees: { joinedAt: string; paid: boolean; earnedMinor: number }[];
};

/**
 * The invite link as a QR code, the choice of days or balance when the
 * programme offers both, what invites earned over a week, a month, and all
 * time, and the friends invited — when each joined, whether they paid, and
 * what they earned the customer. Nothing on it identifies a friend.
 */
export function ReferralInsights({ currency, link }: { currency: string; link: string }) {
  const translate = useTranslations("account.account.referralInsights");
  const money = useMoney();
  const format = useFormatter();
  const choiceId = useId();
  const { data, mutate } = useSWR<Insights, ApiError>("/v1/account/referrals/insights", fetcher);

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

  return (
    <div className="space-y-4">
      {link && (
        <section className="flex flex-col items-center gap-2 rounded-xl border border-border bg-card p-4">
          <QrCode alt={translate("qr")} className="h-40 w-40" size={320} value={link} />
          <p className="text-[12px] text-muted-foreground">{translate("qrHint")}</p>
        </section>
      )}

      {data?.rewardChoice.allowed && (
        <section className="space-y-2 rounded-xl border border-border bg-card p-4">
          <label className="font-medium text-[13px]" htmlFor={choiceId}>
            {translate("choice")}
          </label>
          <Select
            onValueChange={(kind) => void choose(kind)}
            value={data.rewardChoice.kind || "days"}
          >
            <SelectTrigger id={choiceId}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="days">{translate("kinds.days")}</SelectItem>
              <SelectItem value="wallet">{translate("kinds.wallet")}</SelectItem>
            </SelectContent>
          </Select>
        </section>
      )}

      {data && (
        <>
          <SectionLabel>{translate("earnings")}</SectionLabel>
          <div className="grid grid-cols-3 gap-3">
            {(
              [
                ["last7", data.earnings.last7Minor],
                ["last30", data.earnings.last30Minor],
                ["all", data.earnings.allMinor],
              ] as const
            ).map(([period, amount]) => (
              <div className="rounded-xl border border-border bg-card p-3" key={period}>
                <p className="font-mono text-[11px] text-subtle-foreground">
                  {translate(`periods.${period}`)}
                </p>
                <p className="mt-1 font-semibold text-[15px]" data-numeric>
                  {money(amount, currency)}
                </p>
              </div>
            ))}
          </div>

          <SectionLabel>{translate("invitees")}</SectionLabel>
          {data.invitees.length === 0 ? (
            <p className="text-[12.5px] text-muted-foreground">{translate("noInvitees")}</p>
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
      )}
    </div>
  );
}
