"use client";

import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { useTranslations } from "next-intl";
import { useEffect, useId, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { useOperatorAction } from "@/lib/operations";

type Terms = { commissionBps?: number; firstPaymentCommissionBps?: number; note?: string };

const percent = (bps?: number) => (bps === undefined || bps === null ? "" : String(bps / 100));
const toBps = (value: string) =>
  value.trim() === "" ? undefined : Math.round((Number(value.replace(",", ".")) || 0) * 100);

/**
 * Referral administration on the customer card: attach the inviter a link
 * lost, detach one that paid nothing out, and give this customer a personal
 * commission rate as an inviter.
 */
export function ReferralAdminPanel({
  active,
  customerId,
}: {
  active: boolean;
  customerId: string;
}) {
  const translate = useTranslations("admin.customers.referralAdmin");
  const ids = { code: useId(), rate: useId(), first: useId(), note: useId() };
  const path = `/v1/panel/customers/${customerId}`;
  const { data, mutate } = useSWR<Terms, ApiError>(
    active ? `${path}/referral-terms` : null,
    fetcher,
  );
  const [code, setCode] = useState("");
  const [rate, setRate] = useState("");
  const [first, setFirst] = useState("");
  const [note, setNote] = useState("");
  const { run, pending, error } = useOperatorAction();

  useEffect(() => {
    setRate(percent(data?.commissionBps));
    setFirst(percent(data?.firstPaymentCommissionBps));
    setNote(data?.note ?? "");
  }, [data]);

  return (
    <Card className="flex flex-col gap-4 p-4">
      <div className="font-medium">{translate("title")}</div>
      <div className="flex flex-wrap items-end gap-2">
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.code}>{translate("code")}</Label>
          <Input id={ids.code} onChange={(event) => setCode(event.target.value)} value={code} />
        </div>
        <Button
          disabled={pending || !code.trim()}
          onClick={async () => {
            if (
              await run(`${path}/referrer`, {
                method: "POST",
                body: { code: code.trim() },
                reason: "referral attached",
              })
            ) {
              setCode("");
            }
          }}
          variant="outline"
        >
          {translate("attach")}
        </Button>
        <Button
          disabled={pending}
          onClick={() =>
            void run(`${path}/referrer`, { method: "DELETE", reason: "referral detached" })
          }
          variant="outline"
        >
          {translate("detach")}
        </Button>
      </div>
      <div className="grid gap-3 sm:grid-cols-[10rem_10rem_1fr_auto]">
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.rate}>{translate("rate")}</Label>
          <Input
            id={ids.rate}
            inputMode="decimal"
            onChange={(event) => setRate(event.target.value)}
            value={rate}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.first}>{translate("first")}</Label>
          <Input
            id={ids.first}
            inputMode="decimal"
            onChange={(event) => setFirst(event.target.value)}
            value={first}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.note}>{translate("note")}</Label>
          <Input
            id={ids.note}
            maxLength={400}
            onChange={(event) => setNote(event.target.value)}
            value={note}
          />
        </div>
        <Button
          className="self-end"
          disabled={pending}
          onClick={async () => {
            if (
              await run(`${path}/referral-terms`, {
                method: "PUT",
                body: { commissionBps: toBps(rate), firstPaymentCommissionBps: toBps(first), note },
                reason: "referral terms",
              })
            ) {
              void mutate();
            }
          }}
        >
          {translate("save")}
        </Button>
      </div>
      <p className="text-muted-foreground text-xs">{translate("hint")}</p>
      {error && (
        <p className="text-destructive text-sm" role="alert">
          {error.code === "referral_locked"
            ? translate("locked")
            : error.status === 409
              ? translate("conflict")
              : translate("invalid")}
        </p>
      )}
    </Card>
  );
}
