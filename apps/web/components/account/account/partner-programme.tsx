"use client";

import { Button } from "@omniflow/ui/button";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { SectionLabel } from "@/components/account/state";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";

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

const percent = (bps?: number) =>
  bps === undefined || bps === null ? "—" : `${(bps / 100).toLocaleString()}%`;

/**
 * The partner programme: a customer who promotes the service applies, and an
 * operator answers with a personal share of their invitees' payments. The
 * customer's words go to the operator only.
 */
export function PartnerProgramme() {
  const translate = useTranslations("account.account.partner");
  const ids = { channel: useId(), message: useId() };
  const { data, error, mutate } = useSWR<PartnerState, ApiError>(
    "/v1/account/referrals/partner",
    fetcher,
    { shouldRetryOnError: false },
  );
  const [channel, setChannel] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);

  if (error || !data) {
    return null;
  }

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
    <>
      <SectionLabel>{translate("title")}</SectionLabel>
      <section className="space-y-3 rounded-xl border border-border bg-card p-4">
        {data.partner ? (
          <p className="text-[13.5px] leading-relaxed">
            {translate("active", {
              first: percent(data.firstPaymentCommissionBps),
              rate: percent(data.commissionBps),
            })}
          </p>
        ) : data.latest?.status === "pending" ? (
          <p className="text-[13.5px] leading-relaxed">
            {translate("pending")} <Timestamp value={data.latest.createdAt} />
          </p>
        ) : (
          <>
            <p className="text-[13.5px] text-muted-foreground leading-relaxed">
              {data.latest?.status === "rejected" ? `${translate("rejected")} ` : ""}
              {translate("intro")}
            </p>
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
    </>
  );
}
