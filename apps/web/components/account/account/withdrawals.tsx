"use client";

import { Button } from "@omniflow/ui/button";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { AccountNotice, SectionLabel } from "@/components/account/state";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";
import { useSubmission } from "@/lib/idempotency";

type WithdrawalRequest = {
  id: string;
  amountMinor: number;
  currency: string;
  status: "pending" | "approved" | "rejected" | "cancelled" | "paid";
  method: string;
  note?: string;
  createdAt: string;
};

export type WithdrawalSummary = {
  enabled: boolean;
  currency: string;
  minimumMinor: number;
  cooldownDays: number;
  onlyReferralBalance: boolean;
  earnedMinor: number;
  availableMinor: number;
  nextAllowedAt?: string;
  requests: WithdrawalRequest[];
};

const METHODS = ["card", "sbp", "crypto", "other"] as const;

/**
 * The withdrawal policy and the customer's requests, shared by the form and the
 * payout rules so both read one request. A 404 means the installation has no
 * withdrawal service at all, which the payout rules state as "not withdrawable".
 */
export function useWithdrawalSummary() {
  return useSWR<WithdrawalSummary, ApiError>("/v1/account/referrals/withdrawals", fetcher, {
    shouldRetryOnError: false,
  });
}

/**
 * Withdrawing what referrals earned.
 *
 * The amount is held the moment the request is sent, so the balance shown on
 * the wallet drops at once; a rejected or cancelled request gives it back.
 * Nothing renders when the operator has not switched withdrawals on.
 */
export function Withdrawals() {
  const translate = useTranslations("account.account.withdrawals");
  const money = useMoney();
  const { data, mutate } = useWithdrawalSummary();
  const submission = useSubmission();
  const ids = { amount: useId(), method: useId(), details: useId() };
  const [amount, setAmount] = useState("");
  const [method, setMethod] = useState<(typeof METHODS)[number]>("card");
  const [details, setDetails] = useState("");
  const [busy, setBusy] = useState(false);

  if (!data?.enabled) {
    return null;
  }
  const pending = data.requests.find((request) => request.status === "pending");
  const blockedUntil = data.nextAllowedAt ? new Date(data.nextAllowedAt) : null;
  const amountMinor = Math.round(Number(amount.replace(",", ".")) * 100) || 0;
  const canRequest = !pending && !blockedUntil && data.availableMinor >= data.minimumMinor && !busy;

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    try {
      await apiFetch("/v1/account/referrals/withdrawals", {
        body: JSON.stringify({ amountMinor, details: details.trim(), method }),
        headers: { "Idempotency-Key": submission.begin() },
        method: "POST",
      });
      submission.settle();
      setAmount("");
      setDetails("");
      toast.success(translate("sent"));
      void mutate();
    } catch (error) {
      submission.settle(error);
      toast.error((error as ApiError).message ?? translate("failed"));
    } finally {
      setBusy(false);
    }
  }

  async function cancel(id: string) {
    setBusy(true);
    try {
      await apiFetch(`/v1/account/referrals/withdrawals/${id}`, { method: "DELETE" });
      void mutate();
    } catch (error) {
      toast.error((error as ApiError).message ?? translate("failed"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <SectionLabel>{translate("title")}</SectionLabel>
      <section className="space-y-3 rounded-xl border border-border bg-card p-4">
        <p className="text-[13px] text-muted-foreground">
          {translate("available", { amount: money(data.availableMinor, data.currency) })}
          {" · "}
          {translate("minimum", { amount: money(data.minimumMinor, data.currency) })}
        </p>
        {data.onlyReferralBalance && (
          <p className="text-[12.5px] text-muted-foreground">{translate("onlyReferral")}</p>
        )}
        {pending && (
          <AccountNotice description={translate("pendingHint")} title={translate("pending")} />
        )}
        {blockedUntil && !pending && (
          <p className="text-[12.5px] text-muted-foreground">
            {translate("cooldown")} <Timestamp value={data.nextAllowedAt ?? ""} />
          </p>
        )}
        {canRequest && (
          <form className="space-y-3" onSubmit={submit}>
            <div className="space-y-1">
              <Label htmlFor={ids.amount}>{translate("amount")}</Label>
              <Input
                id={ids.amount}
                inputMode="decimal"
                onChange={(event) => setAmount(event.target.value)}
                value={amount}
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor={ids.method}>{translate("method")}</Label>
              <Select onValueChange={(value) => setMethod(value as typeof method)} value={method}>
                <SelectTrigger id={ids.method}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {METHODS.map((option) => (
                    <SelectItem key={option} value={option}>
                      {translate(`methods.${option}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1">
              <Label htmlFor={ids.details}>{translate("details")}</Label>
              <Input
                autoComplete="off"
                id={ids.details}
                maxLength={300}
                onChange={(event) => setDetails(event.target.value)}
                placeholder={translate(`placeholders.${method}`)}
                value={details}
              />
            </div>
            <Button
              className="w-full"
              disabled={
                amountMinor < data.minimumMinor ||
                amountMinor > data.availableMinor ||
                details.trim() === ""
              }
              size="lg"
              type="submit"
            >
              {translate("submit")}
            </Button>
          </form>
        )}
        {data.requests.length > 0 && (
          <ul className="divide-y divide-border">
            {data.requests.map((request) => (
              <li className="flex items-center justify-between gap-3 py-2" key={request.id}>
                <div>
                  <p className="font-semibold text-[14px]" data-numeric>
                    {money(request.amountMinor, request.currency)}
                  </p>
                  <p className="text-[12px] text-muted-foreground">
                    {translate(`statuses.${request.status}`)} ·{" "}
                    <Timestamp value={request.createdAt} />
                    {request.note ? ` · ${request.note}` : ""}
                  </p>
                </div>
                {request.status === "pending" && (
                  <Button
                    disabled={busy}
                    onClick={() => cancel(request.id)}
                    size="sm"
                    variant="ghost"
                  >
                    {translate("cancel")}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>
    </>
  );
}
