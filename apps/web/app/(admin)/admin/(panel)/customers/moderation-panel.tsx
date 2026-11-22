"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Input, Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

type BlockerState = {
  blocked: number;
  lastScan?: {
    status: string;
    checked: number;
    blocked: number;
    requestedAt: string;
    finishedAt?: string;
  };
};
type BanResult = { id: string; customerId?: string; outcome: string };

const NO_TEMPLATE = "none";

/**
 * Moderation on the customer list: ban a list of customers by customer or
 * Telegram ID with a reason and an optional message, and find and clean up
 * the customers who blocked the bot.
 */
export function ModerationPanel() {
  const translate = useTranslations("admin.moderation");
  const { can } = useSession();
  const writable = can("customers.write");
  const ids = { list: useId(), reason: useId(), template: useId(), message: useId() };
  const { data: blockers, mutate } = useSWR<BlockerState, ApiError>(
    "/v1/panel/moderation/blockers",
    fetcher,
    { refreshInterval: 10000 },
  );
  const { data: templates } = useSWR<{ items: { key: string; text: string }[] }, ApiError>(
    "/v1/panel/moderation/ban-templates",
    fetcher,
  );
  const { run, pending } = useOperatorAction();
  const [list, setList] = useState("");
  const [reason, setReason] = useState("");
  const [template, setTemplate] = useState(NO_TEMPLATE);
  const [message, setMessage] = useState("");
  const [results, setResults] = useState<BanResult[] | null>(null);
  const [confirming, setConfirming] = useState(false);
  const targets = list.split(/[\s,;]+/).filter((value) => value.trim() !== "");

  async function ban() {
    try {
      const response = await apiFetch<{ items: BanResult[] }>("/v1/panel/moderation/bans", {
        method: "POST",
        headers: { "X-Operator-Reason": reason.trim() },
        body: JSON.stringify({
          ids: targets,
          reason: reason.trim(),
          template: template === NO_TEMPLATE ? "" : template,
          message: message.trim(),
        }),
      });
      setResults(response.items);
      toast.success(translate("banned"));
      setList("");
    } catch {
      toast.error(translate("failed"));
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-col gap-2 rounded-md border bg-well p-3">
          <div className="font-medium text-sm">{translate("blockers.title")}</div>
          <p className="text-muted-foreground text-sm">
            {translate("blockers.count", { count: blockers?.blocked ?? 0 })}
            {blockers?.lastScan ? (
              <>
                {" · "}
                {translate("blockers.lastScan", {
                  status: blockers.lastScan.status,
                  checked: blockers.lastScan.checked,
                  blocked: blockers.lastScan.blocked,
                })}{" "}
                <Timestamp value={blockers.lastScan.finishedAt ?? blockers.lastScan.requestedAt} />
              </>
            ) : null}
          </p>
          {writable ? (
            <div className="flex flex-wrap gap-2">
              <Button
                disabled={pending}
                onClick={async () => {
                  if (await run("/v1/panel/moderation/blocker-scan", { method: "POST" })) {
                    toast.success(translate("blockers.requested"));
                    await mutate();
                  }
                }}
                size="sm"
                variant="outline"
              >
                {translate("blockers.scan")}
              </Button>
              <Button
                disabled={pending || !blockers?.blocked}
                onClick={async () => {
                  if (
                    await run("/v1/panel/moderation/blockers/clean", {
                      method: "POST",
                      reason: "blocker clean-up",
                    })
                  ) {
                    toast.success(translate("blockers.cleaned"));
                    await mutate();
                  }
                }}
                size="sm"
                variant="outline"
              >
                {translate("blockers.clean")}
              </Button>
            </div>
          ) : null}
        </div>

        {writable ? (
          <div className="flex flex-col gap-3">
            <div className="font-medium text-sm">{translate("ban.title")}</div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.list}>{translate("ban.list")}</Label>
              <Textarea
                className="font-mono text-xs"
                id={ids.list}
                onChange={(event) => setList(event.target.value)}
                rows={4}
                value={list}
              />
              <span className="text-muted-foreground text-xs">
                {translate("ban.count", { count: targets.length })}
              </span>
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="flex flex-col gap-1">
                <Label htmlFor={ids.reason}>{translate("ban.reason")}</Label>
                <Input
                  id={ids.reason}
                  onChange={(event) => setReason(event.target.value)}
                  value={reason}
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor={ids.template}>{translate("ban.template")}</Label>
                <Select onValueChange={setTemplate} value={template}>
                  <SelectTrigger id={ids.template}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={NO_TEMPLATE}>{translate("ban.noMessage")}</SelectItem>
                    {(templates?.items ?? []).map((item) => (
                      <SelectItem key={item.key} value={item.key}>
                        {translate(`ban.templates.${item.key}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.message}>{translate("ban.message")}</Label>
              <Textarea
                id={ids.message}
                onChange={(event) => setMessage(event.target.value)}
                placeholder={templates?.items.find((item) => item.key === template)?.text}
                rows={3}
                value={message}
              />
            </div>
            <Button
              className="self-start"
              disabled={targets.length === 0 || reason.trim() === ""}
              onClick={() => setConfirming(true)}
              variant="destructive"
            >
              {translate("ban.submit")}
            </Button>
            {results ? (
              <p className="text-muted-foreground text-xs">
                {translate("ban.summary", {
                  suspended: results.filter((item) => item.outcome.startsWith("suspended")).length,
                  missing: results.filter((item) => item.outcome === "not_found").length,
                })}
              </p>
            ) : null}
          </div>
        ) : null}
        <ConfirmDialog
          cancelLabel={translate("cancel")}
          confirmLabel={translate("ban.submit")}
          description={translate("ban.confirm", { count: targets.length })}
          destructive
          onConfirm={() => {
            setConfirming(false);
            ban();
          }}
          onOpenChange={setConfirming}
          open={confirming}
          title={translate("ban.title")}
        />
      </CardContent>
    </Card>
  );
}
