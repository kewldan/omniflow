"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { DateTimeField } from "@omniflow/ui/date-time-field";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Switch } from "@omniflow/ui/switch";
import { useLocale, useTranslations } from "next-intl";
import { useEffect, useId, useState } from "react";
import useSWR from "swr";

import { CustomerChip } from "@/components/admin/customer-chip";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import { formatMoney, useOperatorAction } from "@/lib/operations";

type Level2 = { enabled: boolean; percent: number };
type Contest = {
  id: string;
  titleRu: string;
  titleEn: string;
  startsAt: string;
  endsAt: string;
  currency: string;
  prizes: number[];
  finishedAt?: string;
};
type Standing = { place: number; customerId: string; referrals: number };

/**
 * The second referral level and referral contests. A contest's prizes are
 * paid when an operator with finance.write finishes it after it has ended.
 */
export function ReferralLevelsCard({ editable, canPay }: { editable: boolean; canPay: boolean }) {
  const translate = useTranslations("admin.marketing.referralLevels");
  const locale = useLocale();
  const { data, mutate } = useSWR<{ level2: Level2; contests: Contest[] }, ApiError>(
    "/v1/panel/marketing/referral-levels",
    fetcher,
  );
  const { run, pending, error } = useOperatorAction();
  const [level2, setLevel2] = useState<Level2 | null>(null);
  const [form, setForm] = useState({
    titleRu: "",
    titleEn: "",
    startsAt: "",
    endsAt: "",
    prizes: "",
  });
  const [board, setBoard] = useState<{ id: string; items: Standing[] } | null>(null);
  const ids = {
    enabled: useId(),
    percent: useId(),
    title: useId(),
    starts: useId(),
    ends: useId(),
    prizes: useId(),
  };

  useEffect(() => {
    if (data && !level2) {
      setLevel2(data.level2);
    }
  }, [data, level2]);

  async function saveLevel() {
    if (
      level2 &&
      (await run("/v1/panel/marketing/referral-levels", {
        body: level2,
        method: "PUT",
        reason: translate("reason"),
      }))
    ) {
      void mutate();
    }
  }

  async function createContest() {
    const prizes = form.prizes
      .split(/[\s,]+/)
      .map((value) => Math.round(Number(value.replace(",", ".")) * 100))
      .filter((value) => value > 0);
    if (
      await run("/v1/panel/marketing/referral-contests", {
        body: {
          endsAt: new Date(form.endsAt).toISOString(),
          prizes,
          startsAt: new Date(form.startsAt).toISOString(),
          titleEn: form.titleEn || form.titleRu,
          titleRu: form.titleRu,
        },
        method: "POST",
        reason: translate("reason"),
      })
    ) {
      setForm({ titleRu: "", titleEn: "", startsAt: "", endsAt: "", prizes: "" });
      void mutate();
    }
  }

  async function showBoard(contest: Contest) {
    const response = await fetcher<{ items: Standing[] }>(
      `/v1/panel/marketing/referral-contests/${contest.id}/leaderboard`,
    );
    setBoard({ id: contest.id, items: response.items });
  }

  async function finish(contest: Contest) {
    if (
      await run(`/v1/panel/marketing/referral-contests/${contest.id}/finish`, {
        method: "POST",
        reason: translate("finishReason"),
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
      <CardContent className="flex flex-col gap-4">
        {level2 && (
          <div className="flex flex-col gap-4 rounded-md border border-dashed bg-well p-4">
            <div className="flex items-center gap-2">
              <Switch
                checked={level2.enabled}
                disabled={!editable}
                id={ids.enabled}
                onCheckedChange={(enabled) => setLevel2({ ...level2, enabled })}
              />
              <Label htmlFor={ids.enabled}>{translate("level2")}</Label>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={ids.percent}>{translate("percent")}</Label>
              <Input
                className="w-24"
                disabled={!editable}
                id={ids.percent}
                inputMode="numeric"
                onChange={(event) =>
                  setLevel2({ ...level2, percent: Number.parseInt(event.target.value, 10) || 0 })
                }
                value={level2.percent}
              />
            </div>
            {editable && (
              <Button className="self-start" disabled={pending} onClick={saveLevel} type="button">
                {translate("save")}
              </Button>
            )}
          </div>
        )}
        {error && <p className="text-danger-foreground text-xs">{error.message}</p>}
        <p className="font-medium text-sm">{translate("contests")}</p>
        {(data?.contests ?? []).map((contest) => (
          <div className="rounded-md border p-3 text-sm" key={contest.id}>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="font-medium">
                {locale === "ru" ? contest.titleRu : contest.titleEn}
              </span>
              <span className="text-muted-foreground text-xs">
                <Timestamp value={contest.startsAt} /> — <Timestamp value={contest.endsAt} />
              </span>
            </div>
            <p className="text-muted-foreground text-xs">
              {contest.prizes
                .map((prize) => formatMoney(prize, contest.currency, locale))
                .join(" · ")}
              {contest.finishedAt ? ` · ${translate("finished")}` : ""}
            </p>
            <div className="mt-2 flex gap-2">
              <Button onClick={() => showBoard(contest)} size="sm" variant="outline">
                {translate("leaderboard")}
              </Button>
              {canPay && !contest.finishedAt && new Date(contest.endsAt) < new Date() && (
                <Button disabled={pending} onClick={() => finish(contest)} size="sm">
                  {translate("finish")}
                </Button>
              )}
            </div>
            {board?.id === contest.id && (
              <ol className="mt-2 text-xs">
                {board.items.length === 0 && (
                  <li className="text-muted-foreground">{translate("empty")}</li>
                )}
                {board.items.map((standing) => (
                  <li key={standing.customerId}>
                    {standing.place}.{" "}
                    <CustomerChip className="align-middle" id={standing.customerId} /> —{" "}
                    {standing.referrals}
                  </li>
                ))}
              </ol>
            )}
          </div>
        ))}
        {editable && (
          <div className="grid gap-2 rounded-md border border-dashed bg-well p-4 sm:grid-cols-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.title}>{translate("titleRu")}</Label>
              <Input
                id={ids.title}
                onChange={(event) => setForm({ ...form, titleRu: event.target.value })}
                value={form.titleRu}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={`${ids.title}-en`}>{translate("titleEn")}</Label>
              <Input
                id={`${ids.title}-en`}
                onChange={(event) => setForm({ ...form, titleEn: event.target.value })}
                value={form.titleEn}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.starts}>{translate("startsAt")}</Label>
              <DateTimeField
                hourLabel={translate("hour")}
                id={ids.starts}
                minuteLabel={translate("minute")}
                onChange={(startsAt) => setForm({ ...form, startsAt })}
                placeholder={translate("startsAt")}
                value={form.startsAt}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.ends}>{translate("endsAt")}</Label>
              <DateTimeField
                hourLabel={translate("hour")}
                id={ids.ends}
                minuteLabel={translate("minute")}
                onChange={(endsAt) => setForm({ ...form, endsAt })}
                placeholder={translate("endsAt")}
                value={form.endsAt}
              />
            </div>
            <div className="flex flex-col gap-1 sm:col-span-2">
              <Label htmlFor={ids.prizes}>{translate("prizes")}</Label>
              <Input
                id={ids.prizes}
                onChange={(event) => setForm({ ...form, prizes: event.target.value })}
                placeholder="500, 200, 100"
                value={form.prizes}
              />
            </div>
            <Button
              className="self-start justify-self-start"
              disabled={pending || !form.titleRu || !form.startsAt || !form.endsAt || !form.prizes}
              onClick={createContest}
              type="button"
            >
              {translate("create")}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
