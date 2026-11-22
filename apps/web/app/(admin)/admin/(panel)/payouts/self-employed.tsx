"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useLocale, useTranslations } from "next-intl";
import { useEffect, useId, useState } from "react";
import useSWR from "swr";

import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import { formatMoney, useOperatorAction } from "@/lib/operations";

type Settings = {
  enabled: boolean;
  enabledAt?: string;
  inn: string;
  hasPassword: boolean;
  serviceName: string;
  providers: string[];
};

type Receipt = {
  id: string;
  orderId: string;
  amountMinor: number;
  status: "pending" | "uncertain" | "issued" | "failed" | "cancelled";
  attempts: number;
  receiptUrl?: string;
  lastErrorCode?: string;
  operationTime: string;
};

const STATUS_VARIANT = {
  cancelled: "neutral",
  failed: "danger",
  issued: "success",
  pending: "info",
  uncertain: "warning",
} as const;

/**
 * «Мой налог» receipts for an operator registered as self-employed. The
 * password is write-only: the panel shows whether one is stored, never it.
 */
export function SelfEmployedCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.payouts.selfEmployed");
  const locale = useLocale();
  const { data: settings, mutate: refreshSettings } = useSWR<Settings, ApiError>(
    "/v1/panel/finance/self-employed/settings",
    fetcher,
  );
  const { data, isLoading, mutate } = useSWR<{ items: Receipt[] }, ApiError>(
    "/v1/panel/finance/self-employed",
    fetcher,
  );
  const { run, pending, error } = useOperatorAction();
  const [draft, setDraft] = useState<
    (Settings & { password: string; providerList: string }) | null
  >(null);
  const [checked, setChecked] = useState(false);
  const ids = {
    enabled: useId(),
    inn: useId(),
    password: useId(),
    name: useId(),
    providers: useId(),
  };

  useEffect(() => {
    if (settings && !draft) {
      setDraft({ ...settings, password: "", providerList: settings.providers.join(", ") });
    }
  }, [settings, draft]);

  async function save() {
    if (!draft) {
      return;
    }
    const ok = await run("/v1/panel/finance/self-employed/settings", {
      body: {
        enabled: draft.enabled,
        inn: draft.inn.trim(),
        password: draft.password,
        providers: draft.providerList
          .split(/[\s,]+/)
          .map((value) => value.trim())
          .filter(Boolean),
        serviceName: draft.serviceName,
      },
      method: "PUT",
      reason: translate("settingsReason"),
    });
    if (ok) {
      setDraft(null);
      void refreshSettings();
    }
  }

  async function check() {
    setChecked(
      await run("/v1/panel/finance/self-employed/check", {
        method: "POST",
        reason: translate("checkReason"),
      }),
    );
  }

  async function retry(receipt: Receipt) {
    if (
      await run(`/v1/panel/finance/self-employed/${receipt.id}/retry`, {
        method: "POST",
        reason: translate("retryReason"),
      })
    ) {
      void mutate();
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-5">
        {draft && (
          <div className="grid gap-3 rounded-md border border-dashed bg-well p-4 sm:grid-cols-2">
            <div className="flex items-center gap-2 sm:col-span-2">
              <Switch
                checked={draft.enabled}
                disabled={!editable}
                id={ids.enabled}
                onCheckedChange={(enabled) => setDraft({ ...draft, enabled })}
              />
              <Label htmlFor={ids.enabled}>{translate("enabled")}</Label>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.inn}>{translate("inn")}</Label>
              <Input
                disabled={!editable}
                id={ids.inn}
                inputMode="numeric"
                maxLength={12}
                onChange={(event) => setDraft({ ...draft, inn: event.target.value })}
                value={draft.inn}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.password}>{translate("password")}</Label>
              <Input
                autoComplete="new-password"
                disabled={!editable}
                id={ids.password}
                onChange={(event) => setDraft({ ...draft, password: event.target.value })}
                placeholder={draft.hasPassword ? translate("passwordKept") : ""}
                type="password"
                value={draft.password}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.name}>{translate("serviceName")}</Label>
              <Input
                disabled={!editable}
                id={ids.name}
                maxLength={128}
                onChange={(event) => setDraft({ ...draft, serviceName: event.target.value })}
                value={draft.serviceName}
              />
            </div>
            <div className="flex flex-col gap-1 sm:col-span-2">
              <Label htmlFor={ids.providers}>{translate("providers")}</Label>
              <Input
                disabled={!editable}
                id={ids.providers}
                onChange={(event) => setDraft({ ...draft, providerList: event.target.value })}
                placeholder="yookassa, cloudpayments"
                value={draft.providerList}
              />
              <p className="text-muted-foreground text-xs">{translate("providersHint")}</p>
            </div>
            {editable && (
              <div className="flex flex-wrap items-center gap-2 sm:col-span-2">
                <Button disabled={pending} onClick={save} type="button">
                  {translate("save")}
                </Button>
                <Button
                  disabled={pending || !settings?.hasPassword}
                  onClick={check}
                  type="button"
                  variant="outline"
                >
                  {translate("check")}
                </Button>
                {checked && (
                  <span className="text-muted-foreground text-xs">{translate("checkOk")}</span>
                )}
                {error && <span className="text-danger-foreground text-xs">{error.message}</span>}
              </div>
            )}
          </div>
        )}

        {isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : (data?.items ?? []).length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("empty")}</p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("columns.order")}</TableHead>
                  <TableHead className="text-right">{translate("columns.amount")}</TableHead>
                  <TableHead>{translate("columns.status")}</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {data?.items.map((receipt) => (
                  <TableRow key={receipt.id}>
                    <TableCell>
                      <span className="font-mono text-xs">{receipt.orderId.slice(0, 8)}</span>
                      <p className="text-muted-foreground text-xs">
                        <Timestamp value={receipt.operationTime} />
                      </p>
                    </TableCell>
                    <TableCell className="text-right">
                      {formatMoney(receipt.amountMinor, "RUB", locale)}
                    </TableCell>
                    <TableCell>
                      <Badge variant={STATUS_VARIANT[receipt.status]}>
                        {translate(`statuses.${receipt.status}`)}
                      </Badge>
                      {receipt.lastErrorCode && (
                        <p className="text-muted-foreground text-xs">
                          {receipt.lastErrorCode} ·{" "}
                          {translate("attempts", { count: receipt.attempts })}
                        </p>
                      )}
                    </TableCell>
                    <TableCell className="text-right">
                      {receipt.receiptUrl ? (
                        <a
                          className="text-xs underline"
                          href={receipt.receiptUrl}
                          rel="noreferrer noopener"
                          target="_blank"
                        >
                          {translate("open")}
                        </a>
                      ) : editable && receipt.status === "failed" ? (
                        <Button
                          disabled={pending}
                          onClick={() => retry(receipt)}
                          size="sm"
                          variant="outline"
                        >
                          {translate("retry")}
                        </Button>
                      ) : null}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
