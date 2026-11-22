"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useLocale, useTranslations } from "next-intl";
import { useEffect, useId, useState } from "react";
import useSWR from "swr";

import { CustomerChip } from "@/components/admin/customer-chip";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { formatMoney, useOperatorAction } from "@/lib/operations";

type Settings = {
  enabled: boolean;
  currency: string;
  minimumMinor: number;
  cooldownDays: number;
  onlyReferralBalance: boolean;
};

type Withdrawal = {
  id: string;
  customerId: string;
  amountMinor: number;
  currency: string;
  status: string;
  method: string;
  note?: string;
  risk: { score: number; level: "low" | "medium" | "high" | "critical"; flags: string[] };
  createdAt: string;
};

const LEVEL_VARIANT = {
  critical: "danger",
  high: "warning",
  low: "success",
  medium: "info",
} as const;

/**
 * The referral withdrawal queue and its policy.
 *
 * The risk score is advice, shown with the reasons behind it; nothing is
 * decided automatically. Payout details are hidden until an operator asks
 * for them, and asking is recorded in the audit trail.
 */
export function WithdrawalsCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.payouts.withdrawals");
  const locale = useLocale();
  const [status, setStatus] = useState("pending");
  const { data, isLoading, mutate } = useSWR<{ items: Withdrawal[] }, ApiError>(
    `/v1/panel/finance/withdrawals?status=${status === "all" ? "" : status}`,
    fetcher,
  );
  const { data: settings, mutate: refreshSettings } = useSWR<Settings, ApiError>(
    "/v1/panel/finance/withdrawals/settings",
    fetcher,
  );
  const { run, pending } = useOperatorAction();
  const [draft, setDraft] = useState<Settings | null>(null);
  const [notes, setNotes] = useState<Record<string, string>>({});
  const [revealed, setRevealed] = useState<Record<string, string>>({});
  const ids = {
    enabled: useId(),
    minimum: useId(),
    cooldown: useId(),
    only: useId(),
    status: useId(),
  };

  useEffect(() => {
    if (settings && !draft) {
      setDraft(settings);
    }
  }, [settings, draft]);

  async function saveSettings() {
    if (!draft) {
      return;
    }
    if (
      await run("/v1/panel/finance/withdrawals/settings", {
        body: draft,
        method: "PUT",
        reason: translate("settingsReason"),
      })
    ) {
      void refreshSettings();
    }
  }

  async function decide(item: Withdrawal, action: "approve" | "reject" | "paid") {
    if (
      await run(`/v1/panel/finance/withdrawals/${item.id}/${action}`, {
        body: { note: notes[item.id] ?? "" },
        method: "POST",
        reason: translate(`reasons.${action}`),
      })
    ) {
      void mutate();
    }
  }

  async function reveal(item: Withdrawal) {
    try {
      const result = await apiFetch<{ details: string }>(
        `/v1/panel/finance/withdrawals/${item.id}/details`,
        { headers: { "X-Operator-Reason": translate("reasons.reveal") }, method: "POST" },
      );
      setRevealed({ ...revealed, [item.id]: result.details });
    } catch {
      setRevealed({ ...revealed, [item.id]: translate("revealFailed") });
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
          <div className="flex flex-col gap-4 rounded-md border border-dashed bg-well p-4">
            <div className="flex flex-wrap gap-x-8 gap-y-3">
              <div className="flex items-center gap-2">
                <Switch
                  checked={draft.enabled}
                  disabled={!editable}
                  id={ids.enabled}
                  onCheckedChange={(enabled) => setDraft({ ...draft, enabled })}
                />
                <Label htmlFor={ids.enabled}>{translate("enabled")}</Label>
              </div>
              <div className="flex items-center gap-2">
                <Switch
                  checked={draft.onlyReferralBalance}
                  disabled={!editable}
                  id={ids.only}
                  onCheckedChange={(onlyReferralBalance) =>
                    setDraft({ ...draft, onlyReferralBalance })
                  }
                />
                <Label htmlFor={ids.only}>{translate("onlyReferral")}</Label>
              </div>
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={ids.minimum}>
                  {translate("minimum", { currency: draft.currency })}
                </Label>
                <Input
                  disabled={!editable}
                  id={ids.minimum}
                  inputMode="decimal"
                  onChange={(event) =>
                    setDraft({
                      ...draft,
                      minimumMinor: Math.round(Number(event.target.value) * 100) || 0,
                    })
                  }
                  value={draft.minimumMinor / 100}
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={ids.cooldown}>{translate("cooldown")}</Label>
                <Input
                  disabled={!editable}
                  id={ids.cooldown}
                  inputMode="numeric"
                  onChange={(event) =>
                    setDraft({
                      ...draft,
                      cooldownDays: Number.parseInt(event.target.value, 10) || 0,
                    })
                  }
                  value={draft.cooldownDays}
                />
              </div>
            </div>
            {editable && (
              <div>
                <Button disabled={pending} onClick={saveSettings} type="button">
                  {translate("save")}
                </Button>
              </div>
            )}
          </div>
        )}

        <div className="flex items-center gap-2">
          <Label htmlFor={ids.status}>{translate("filter")}</Label>
          <Select onValueChange={setStatus} value={status}>
            <SelectTrigger className="w-auto min-w-48" id={ids.status}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {["pending", "approved", "paid", "rejected", "cancelled", "all"].map((value) => (
                <SelectItem key={value} value={value}>
                  {translate(`statuses.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        {isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : (data?.items ?? []).length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("empty")}</p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{translate("columns.customer")}</TableHead>
                  <TableHead className="text-right">{translate("columns.amount")}</TableHead>
                  <TableHead>{translate("columns.risk")}</TableHead>
                  <TableHead>{translate("columns.details")}</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {data?.items.map((item) => (
                  <TableRow key={item.id}>
                    <TableCell>
                      <CustomerChip id={item.customerId} />
                      <p className="text-muted-foreground text-xs">
                        <Timestamp value={item.createdAt} /> ·{" "}
                        {translate(`statuses.${item.status}`)}
                      </p>
                    </TableCell>
                    <TableCell className="text-right">
                      {formatMoney(item.amountMinor, item.currency, locale)}
                    </TableCell>
                    <TableCell>
                      <Badge variant={LEVEL_VARIANT[item.risk.level]}>
                        {translate(`levels.${item.risk.level}`)} · {item.risk.score}
                      </Badge>
                      <ul className="mt-1 text-muted-foreground text-xs">
                        {item.risk.flags.map((flag) => (
                          <li key={flag}>{translate(`flags.${flag}`)}</li>
                        ))}
                      </ul>
                    </TableCell>
                    <TableCell className="text-xs">
                      {translate(`methods.${item.method}`)}
                      {revealed[item.id] ? (
                        <p className="font-mono">{revealed[item.id]}</p>
                      ) : editable ? (
                        <Button onClick={() => reveal(item)} size="sm" type="button" variant="link">
                          {translate("reveal")}
                        </Button>
                      ) : null}
                      {item.note && <p className="text-muted-foreground">{item.note}</p>}
                    </TableCell>
                    <TableCell className="text-right">
                      {editable && (item.status === "pending" || item.status === "approved") && (
                        <div className="flex flex-col items-end gap-2">
                          <Input
                            aria-label={translate("note")}
                            className="w-48"
                            onChange={(event) =>
                              setNotes({ ...notes, [item.id]: event.target.value })
                            }
                            placeholder={translate("note")}
                            value={notes[item.id] ?? ""}
                          />
                          <div className="flex gap-2">
                            {item.status === "pending" ? (
                              <>
                                <Button
                                  disabled={pending}
                                  onClick={() => decide(item, "approve")}
                                  size="sm"
                                >
                                  {translate("approve")}
                                </Button>
                                <Button
                                  disabled={pending || !(notes[item.id] ?? "").trim()}
                                  onClick={() => decide(item, "reject")}
                                  size="sm"
                                  variant="outline"
                                >
                                  {translate("reject")}
                                </Button>
                              </>
                            ) : (
                              <Button
                                disabled={pending}
                                onClick={() => decide(item, "paid")}
                                size="sm"
                              >
                                {translate("markPaid")}
                              </Button>
                            )}
                          </div>
                        </div>
                      )}
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
