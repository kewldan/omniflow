"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { DateTimeField } from "@omniflow/ui/date-time-field";
import { Input, Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Switch } from "@omniflow/ui/switch";
import { useTranslations } from "next-intl";
import { useEffect, useId, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { newIdempotencyKey } from "@/lib/idempotency";
import { useOperatorAction } from "@/lib/operations";

type Settings = {
  currency: string;
  daysPlanVersionId: string;
  dayValueMinor: number;
  wheelEnabled: boolean;
  spinCostMinor: number;
  freeSpinsPerDay: number;
  maxSpinsPerDay: number;
  targetRtpPercent: number;
  minimumAccountAgeHours: number;
};
type Prize = {
  id?: string;
  labelRu: string;
  labelEn: string;
  kind: "wallet" | "days" | "nothing";
  value: number;
  weight: number;
  active: boolean;
};
type Poll = {
  id: string;
  questionRu: string;
  questionEn: string;
  options: { ru: string; en: string }[];
  counts?: number[];
  closedAt?: string;
  endsAt: string;
};
type State = { settings: Settings; prizes: Prize[]; polls: Poll[]; rtpPercent: number };

const NUMBER_FIELDS = [
  "spinCostMinor",
  "freeSpinsPerDay",
  "maxSpinsPerDay",
  "targetRtpPercent",
  "dayValueMinor",
  "minimumAccountAgeHours",
] as const;

/**
 * The fortune wheel and polls with rewards. The prize table is checked
 * against the target return before it is saved; every spin and answer is a
 * row the API keeps, and every change here is audited.
 */
export function EngagementCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.marketing.engagement");
  const { data, mutate } = useSWR<State, ApiError>("/v1/panel/marketing/engagement", fetcher);
  const { run, pending, error } = useOperatorAction();
  const [settings, setSettings] = useState<Settings | null>(null);
  const [prizes, setPrizes] = useState<Prize[]>([]);
  const [poll, setPoll] = useState({
    questionRu: "",
    questionEn: "",
    options: "",
    reward: "0",
    endsAt: "",
  });
  const ids = {
    enabled: useId(),
    plan: useId(),
    question: useId(),
    options: useId(),
    reward: useId(),
    ends: useId(),
  };

  useEffect(() => {
    if (data && !settings) {
      setSettings(data.settings);
      setPrizes(data.prizes);
    }
  }, [data, settings]);

  async function saveWheel() {
    if (
      settings &&
      (await run("/v1/panel/marketing/engagement/wheel", {
        body: { prizes, settings },
        method: "PUT",
        reason: translate("wheelReason"),
      }))
    ) {
      setSettings(null);
      void mutate();
    }
  }

  async function createPoll() {
    const options = poll.options
      .split("\n")
      .map((line) => line.split("|").map((part) => part.trim()))
      .filter((parts) => parts[0])
      .map(([ru, en]) => ({ en: en || ru, ru }));
    const reward = Math.round(Number(poll.reward.replace(",", ".")) * 100) || 0;
    if (
      await run("/v1/panel/marketing/engagement/polls", {
        body: {
          endsAt: new Date(poll.endsAt).toISOString(),
          options,
          questionEn: poll.questionEn || poll.questionRu,
          questionRu: poll.questionRu,
          rewardKind: reward > 0 ? "wallet" : "none",
          rewardValue: reward,
        },
        method: "POST",
        reason: translate("pollReason"),
      })
    ) {
      setPoll({ questionRu: "", questionEn: "", options: "", reward: "0", endsAt: "" });
      void mutate();
    }
  }

  async function closePoll(id: string) {
    if (
      await run(`/v1/panel/marketing/engagement/polls/${id}/close`, {
        method: "POST",
        reason: translate("pollReason"),
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
        {settings && (
          <div className="flex flex-col gap-3 rounded-md border border-dashed bg-well p-4">
            <div className="flex items-center gap-2">
              <Switch
                checked={settings.wheelEnabled}
                disabled={!editable}
                id={ids.enabled}
                onCheckedChange={(wheelEnabled) => setSettings({ ...settings, wheelEnabled })}
              />
              <Label htmlFor={ids.enabled}>{translate("wheelEnabled")}</Label>
              <Badge variant="outline">
                {translate("rtp", { rtp: Math.round(data?.rtpPercent ?? 0) })}
              </Badge>
            </div>
            <div className="grid gap-3 sm:grid-cols-3">
              {NUMBER_FIELDS.map((field) => (
                <div className="flex flex-col gap-1" key={field}>
                  <Label htmlFor={`${ids.enabled}-${field}`}>{translate(`fields.${field}`)}</Label>
                  <Input
                    disabled={!editable}
                    id={`${ids.enabled}-${field}`}
                    inputMode="numeric"
                    onChange={(event) =>
                      setSettings({
                        ...settings,
                        [field]: Number.parseInt(event.target.value, 10) || 0,
                      })
                    }
                    value={settings[field]}
                  />
                </div>
              ))}
              <div className="flex flex-col gap-1 sm:col-span-3">
                <Label htmlFor={ids.plan}>{translate("fields.daysPlanVersionId")}</Label>
                <Input
                  disabled={!editable}
                  id={ids.plan}
                  onChange={(event) =>
                    setSettings({ ...settings, daysPlanVersionId: event.target.value.trim() })
                  }
                  value={settings.daysPlanVersionId}
                />
              </div>
            </div>
            <p className="font-medium text-sm">{translate("prizes")}</p>
            {prizes.map((prize, index) => (
              <div
                className="grid gap-2 sm:grid-cols-6"
                key={prize.id ?? `${prize.labelRu}-${prize.kind}`}
              >
                <Input
                  aria-label={translate("labelRu")}
                  disabled={!editable}
                  onChange={(event) =>
                    setPrizes(
                      prizes.map((item, at) =>
                        at === index ? { ...item, labelRu: event.target.value } : item,
                      ),
                    )
                  }
                  placeholder={translate("labelRu")}
                  value={prize.labelRu}
                />
                <Input
                  aria-label={translate("labelEn")}
                  disabled={!editable}
                  onChange={(event) =>
                    setPrizes(
                      prizes.map((item, at) =>
                        at === index ? { ...item, labelEn: event.target.value } : item,
                      ),
                    )
                  }
                  placeholder={translate("labelEn")}
                  value={prize.labelEn}
                />
                <Select
                  disabled={!editable}
                  onValueChange={(kind) =>
                    setPrizes(
                      prizes.map((item, at) =>
                        at === index
                          ? {
                              ...item,
                              kind: kind as Prize["kind"],
                              value: kind === "nothing" ? 0 : item.value,
                            }
                          : item,
                      ),
                    )
                  }
                  value={prize.kind}
                >
                  <SelectTrigger aria-label={translate("kind")}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="wallet">{translate("kinds.wallet")}</SelectItem>
                    <SelectItem value="days">{translate("kinds.days")}</SelectItem>
                    <SelectItem value="nothing">{translate("kinds.nothing")}</SelectItem>
                  </SelectContent>
                </Select>
                <Input
                  aria-label={translate("value")}
                  disabled={!editable || prize.kind === "nothing"}
                  inputMode="numeric"
                  onChange={(event) =>
                    setPrizes(
                      prizes.map((item, at) =>
                        at === index
                          ? { ...item, value: Number.parseInt(event.target.value, 10) || 0 }
                          : item,
                      ),
                    )
                  }
                  placeholder={translate("value")}
                  value={prize.value}
                />
                <Input
                  aria-label={translate("weight")}
                  disabled={!editable}
                  inputMode="numeric"
                  onChange={(event) =>
                    setPrizes(
                      prizes.map((item, at) =>
                        at === index
                          ? { ...item, weight: Number.parseInt(event.target.value, 10) || 0 }
                          : item,
                      ),
                    )
                  }
                  placeholder={translate("weight")}
                  value={prize.weight}
                />
                {editable && (
                  <Button
                    onClick={() => setPrizes(prizes.filter((_, at) => at !== index))}
                    size="sm"
                    type="button"
                    variant="ghost"
                  >
                    {translate("remove")}
                  </Button>
                )}
              </div>
            ))}
            {editable && (
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  onClick={() =>
                    setPrizes([
                      ...prizes,
                      {
                        active: true,
                        id: newIdempotencyKey(),
                        kind: "nothing",
                        labelEn: "",
                        labelRu: "",
                        value: 0,
                        weight: 1,
                      },
                    ])
                  }
                  size="sm"
                  type="button"
                  variant="outline"
                >
                  {translate("addPrize")}
                </Button>
                <Button disabled={pending} onClick={saveWheel} type="button">
                  {translate("saveWheel")}
                </Button>
                {error && <span className="text-danger-foreground text-xs">{error.message}</span>}
              </div>
            )}
          </div>
        )}

        <div className="flex flex-col gap-3">
          <p className="font-medium text-sm">{translate("polls")}</p>
          {(data?.polls ?? []).map((item) => (
            <div className="rounded-md border p-3 text-sm" key={item.id}>
              <div className="flex items-center justify-between gap-2">
                <span className="font-medium">{item.questionRu}</span>
                {editable && !item.closedAt && (
                  <Button
                    disabled={pending}
                    onClick={() => closePoll(item.id)}
                    size="sm"
                    variant="outline"
                  >
                    {translate("close")}
                  </Button>
                )}
              </div>
              <ul className="mt-1 text-muted-foreground text-xs">
                {item.options.map((option, index) => (
                  <li key={option.ru}>
                    {option.ru}: {item.counts?.[index] ?? 0}
                  </li>
                ))}
              </ul>
            </div>
          ))}
          {editable && (
            <div className="grid gap-2 rounded-md border border-dashed bg-well p-4">
              <Label htmlFor={ids.question}>{translate("question")}</Label>
              <Input
                id={ids.question}
                onChange={(event) => setPoll({ ...poll, questionRu: event.target.value })}
                value={poll.questionRu}
              />
              <Input
                aria-label={translate("questionEn")}
                onChange={(event) => setPoll({ ...poll, questionEn: event.target.value })}
                placeholder={translate("questionEn")}
                value={poll.questionEn}
              />
              <Label htmlFor={ids.options}>{translate("options")}</Label>
              <Textarea
                rows={4}
                id={ids.options}
                onChange={(event) => setPoll({ ...poll, options: event.target.value })}
                placeholder={translate("optionsHint")}
                value={poll.options}
              />
              <Label htmlFor={ids.reward}>{translate("reward")}</Label>
              <Input
                id={ids.reward}
                inputMode="decimal"
                onChange={(event) => setPoll({ ...poll, reward: event.target.value })}
                value={poll.reward}
              />
              <Label htmlFor={ids.ends}>{translate("endsAt")}</Label>
              <DateTimeField
                fromDate={new Date()}
                hourLabel={translate("hour")}
                id={ids.ends}
                minuteLabel={translate("minute")}
                onChange={(endsAt) => setPoll({ ...poll, endsAt })}
                placeholder={translate("endsAt")}
                value={poll.endsAt}
              />
              <Button
                className="self-start"
                disabled={pending || !poll.questionRu || !poll.endsAt}
                onClick={createPoll}
                type="button"
              >
                {translate("createPoll")}
              </Button>
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
