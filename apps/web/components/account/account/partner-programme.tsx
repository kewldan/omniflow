"use client";

import { Button } from "@omniflow/ui/button";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { useWithdrawability } from "@/components/account/account/payout-terms";
import { AccountNotice } from "@/components/account/state";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";

type Application = {
  id: string;
  status: "pending" | "approved" | "rejected";
  createdAt: string;
};

type PartnerState = {
  partner: boolean;
  commissionBps?: number;
  firstPaymentCommissionBps?: number;
  latest?: Application;
  canApply: boolean;
};

const percent = (bps: number) => `${(bps / 100).toLocaleString()}%`;

/**
 * The partner state, shared with the switch that decides whether a partner
 * place exists at all. A 404 is an installation without the programme.
 */
export function usePartnerState() {
  return useSWR<PartnerState, ApiError>("/v1/account/referrals/partner", fetcher, {
    shouldRetryOnError: false,
  });
}

/**
 * The partner programme: a customer who promotes the service applies, and an
 * operator answers with a personal share of their invitees' payments. The
 * customer's words go to the operator only.
 *
 * The share is a referral reward of role `commission`, credited to the wallet
 * (`internal/commercepg/commission.go`), and the screen says so under the
 * status. Whether it can be withdrawn is said only when the withdrawal policy
 * covers the whole balance: with "only referral earnings" switched on, the
 * withdrawable amount is counted from invite rewards alone, and the screen
 * makes no promise the server would not keep.
 */
export function PartnerProgramme() {
  const translate = useTranslations("account.account.partner");
  const states = useTranslations("account.account.states");
  const money = useMoney();
  const ids = { channel: useId(), message: useId() };
  const { data, error, isLoading, mutate } = usePartnerState();
  const withdrawable = useWithdrawability();
  const [channel, setChannel] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);

  if (isLoading) {
    return null;
  }
  if (error?.status === 404) {
    return (
      <AccountNotice
        description={translate("unavailableDescription")}
        title={translate("unavailable")}
      />
    );
  }
  if (error || !data) {
    return (
      <AccountNotice
        description={states("loadErrorDescription")}
        title={states("loadError")}
        variant="danger"
      />
    );
  }

  const paidTo =
    withdrawable.state === "yes" && !withdrawable.onlyReferral
      ? translate("paidToWithdrawable", {
          minimum: money(withdrawable.minimumMinor, withdrawable.currency),
        })
      : withdrawable.state === "no"
        ? translate("paidToWalletOnly")
        : translate("paidTo");

  async function apply(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    try {
      await apiFetch("/v1/account/referrals/partner", {
        body: JSON.stringify({ channel: channel.trim(), message: message.trim() }),
        method: "POST",
      });
      toast.success(translate("sent"));
      setChannel("");
      setMessage("");
      void mutate();
    } catch (failure) {
      toast.error((failure as ApiError).message ?? translate("failed"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="space-y-3 rounded-xl border border-border bg-card p-4">
      {data.partner ? (
        <>
          <p className="font-medium text-[14.5px] leading-snug">
            {data.firstPaymentCommissionBps !== undefined &&
            data.firstPaymentCommissionBps !== null &&
            data.firstPaymentCommissionBps !== data.commissionBps
              ? translate("activeWithFirst", {
                  first: percent(data.firstPaymentCommissionBps),
                  rate: percent(data.commissionBps ?? 0),
                })
              : translate("active", { rate: percent(data.commissionBps ?? 0) })}
          </p>
          <p className="text-[12.5px] text-muted-foreground leading-relaxed">{paidTo}</p>
        </>
      ) : data.latest?.status === "pending" ? (
        <p className="text-[13.5px] leading-relaxed">
          {translate("pending")} <Timestamp value={data.latest.createdAt} />
        </p>
      ) : (
        <>
          {data.latest?.status === "rejected" && (
            <p className="text-[12.5px] text-muted-foreground leading-relaxed">
              {translate("rejected")}
            </p>
          )}
          <p className="font-medium text-[14.5px] leading-snug">{translate("intro")}</p>
          <p className="text-[12.5px] text-muted-foreground leading-relaxed">{paidTo}</p>
          {data.canApply && (
            <form className="space-y-3" onSubmit={apply}>
              <div className="space-y-1">
                <Label htmlFor={ids.channel}>{translate("channel")}</Label>
                <Input
                  id={ids.channel}
                  maxLength={200}
                  onChange={(event) => setChannel(event.target.value)}
                  placeholder={translate("channelPlaceholder")}
                  value={channel}
                />
              </div>
              <div className="space-y-1">
                <Label htmlFor={ids.message}>{translate("message")}</Label>
                <Input
                  id={ids.message}
                  maxLength={1000}
                  onChange={(event) => setMessage(event.target.value)}
                  value={message}
                />
              </div>
              <Button className="w-full" disabled={busy} size="lg" type="submit">
                {translate("apply")}
              </Button>
            </form>
          )}
        </>
      )}
    </section>
  );
}
