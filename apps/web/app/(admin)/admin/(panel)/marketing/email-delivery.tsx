"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { useOperatorAction } from "@/lib/operations";

type EmailStatus = {
  sending: boolean;
  receipts: boolean;
  pending: number;
  sent24h: number;
  failed24h: number;
  skipped24h: Record<string, number>;
};

/**
 * Outgoing e-mail: whether a transport is configured, what the queue holds,
 * and a test message to an address the operator types. The transport itself
 * is configured in the environment (Resend or SMTP), so this card reports it
 * rather than editing it.
 */
export function EmailDeliveryCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.marketing.email");
  const locale = useLocale();
  const { data, error, isLoading, mutate } = useSWR<EmailStatus, ApiError>(
    "/v1/panel/settings/email",
    fetcher,
  );
  const { run, pending } = useOperatorAction();
  const addressId = useId();
  const [address, setAddress] = useState("");
  const [queued, setQueued] = useState(false);

  async function sendTest(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const ok = await run("/v1/panel/settings/email/test", {
      body: { address: address.trim(), locale },
      method: "POST",
      reason: translate("testReason"),
    });
    if (ok) {
      setQueued(true);
      void mutate();
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : error || !data ? (
          <p className="text-danger-foreground text-sm">{translate("unavailable")}</p>
        ) : (
          <div className="flex flex-wrap items-center gap-3 text-sm">
            <Badge variant={data.sending ? "success" : "warning"}>
              {data.sending ? translate("configured") : translate("notConfigured")}
            </Badge>
            <Badge variant="outline">
              {data.receipts ? translate("receiptsOn") : translate("receiptsOff")}
            </Badge>
            <span>
              {translate("counts", {
                failed: data.failed24h,
                pending: data.pending,
                sent: data.sent24h,
              })}
            </span>
            {Object.entries(data.skipped24h ?? {}).map(([reason, count]) => (
              <Badge key={reason} variant="neutral">
                {translate("skipped", { count, reason })}
              </Badge>
            ))}
          </div>
        )}
        {editable && (
          <form className="flex flex-wrap items-end gap-3" onSubmit={sendTest}>
            <div className="flex min-w-64 flex-col gap-1">
              <Label htmlFor={addressId}>{translate("testAddress")}</Label>
              <Input
                autoComplete="email"
                id={addressId}
                inputMode="email"
                onChange={(event) => {
                  setAddress(event.target.value);
                  setQueued(false);
                }}
                type="email"
                value={address}
              />
            </div>
            <Button disabled={pending || !address.includes("@")} type="submit" variant="outline">
              {translate("sendTest")}
            </Button>
            {queued && (
              <span className="text-muted-foreground text-xs">{translate("testQueued")}</span>
            )}
          </form>
        )}
      </CardContent>
    </Card>
  );
}
