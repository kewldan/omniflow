"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { ReasonDialog } from "@/components/admin/reason-dialog";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { operatorReasonHeaders, useOperatorAction } from "@/lib/operations";
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
const BAN_REASONS = ["fraud", "multiaccount", "sharing", "abuse", "blocklist"] as const;

/**
 * Moderation on the customer list: ban a list of customers by customer or
 * Telegram ID — the reason and the optional message are given in the dialog
 * that confirms it — and find and clean up the customers who blocked the bot.
 */
export function ModerationPanel() {
  const translate = useTranslations("admin.moderation");
  const presets = useTranslations("admin.reasons.massBan");
  const { can } = useSession();
  const writable = can("customers.write");
  const ids = { list: useId(), template: useId(), message: useId() };
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
  const [template, setTemplate] = useState(NO_TEMPLATE);
  const [message, setMessage] = useState("");
  const [results, setResults] = useState<BanResult[] | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [banning, setBanning] = useState(false);
  const [banError, setBanError] = useState<string | null>(null);
  const targets = list.split(/[\s,;]+/).filter((value) => value.trim() !== "");

  async function ban(reason: string) {
    setBanning(true);
    setBanError(null);
    try {
      const response = await apiFetch<{ items: BanResult[] }>("/v1/panel/moderation/bans", {
        method: "POST",
        headers: operatorReasonHeaders(reason),
        body: JSON.stringify({
          ids: targets,
          reason,
          template: template === NO_TEMPLATE ? "" : template,
          message: message.trim(),
        }),
      });
      setResults(response.items);
      toast.success(translate("banned"));
      setConfirming(false);
      setList("");
      setMessage("");
      setTemplate(NO_TEMPLATE);
    } catch {
      setBanError(translate("failed"));
    } finally {
      setBanning(false);
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
            <Button
              className="self-start"
              disabled={targets.length === 0 || banning}
              onClick={() => {
                setBanError(null);
                setConfirming(true);
              }}
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
        <ReasonDialog
          confirmLabel={translate("ban.submit")}
          description={translate("ban.confirm", { count: targets.length })}
          destructive
          error={banError}
          onConfirm={ban}
          onOpenChange={setConfirming}
          open={confirming}
          pending={banning}
          presets={BAN_REASONS.map((key) => presets(key))}
          title={translate("ban.title")}
        >
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
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.message}>{translate("ban.message")}</Label>
            <Textarea
              id={ids.message}
              maxLength={3500}
              onChange={(event) => setMessage(event.target.value)}
              placeholder={templates?.items.find((item) => item.key === template)?.text}
              rows={3}
              value={message}
            />
          </div>
        </ReasonDialog>
      </CardContent>
    </Card>
  );
}
