"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { DateTimeField } from "@omniflow/ui/date-time-field";
import { Input, Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { useLocale, useTranslations } from "next-intl";
import { useEffect, useId, useState } from "react";
import useSWR from "swr";

import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import { formatMoney, useOperatorAction } from "@/lib/operations";

type Game = {
  code: string;
  enabled: boolean;
  prizeKind: "wallet" | "days" | "promo";
  prizeValue: number;
  prizeTemplateId?: string;
  playsPerDay: number;
  parameter: number;
  costMinor: number;
  targetRtpPercent: number;
  dailyBudgetMinor: number;
  dailyPrizeLimit: number;
  winPercent: number;
  rtpPercent: number;
  playsToday: number;
  winsToday: number;
  paidTodayMinor: number;
};

type Question = {
  id: string;
  game: "quiz" | "first_to_answer";
  questionRu: string;
  questionEn: string;
  options: { ru: string; en: string }[];
  correctIndex: number;
  opensAt: string;
  archivedAt?: string;
  answers: number;
  winners: number;
};

type Template = { id: string; code: string; titleRu: string };

const PARAMETER_GAMES = ["dice", "guess_number", "daily_streak", "first_to_answer"];
const ONCE_A_DAY = ["quiz", "daily_streak", "first_to_answer"];
const CURRENCY = "RUB";

const major = (minor: number) => String(minor / 100);
const minor = (value: string) => Math.round((Number(value.replace(",", ".")) || 0) * 100);

/**
 * Daily games: seven kinds, each switched on with its own prize, daily
 * limits, and an optional entry fee held under a target return. The figures
 * are today's, in UTC. Questions feed the quiz and first-to-answer rounds.
 */
export function DailyGamesCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.marketing.games");
  const { data, isLoading, mutate } = useSWR<{ games: Game[]; questions: Question[] }, ApiError>(
    "/v1/panel/marketing/games",
    fetcher,
  );
  const { data: templates } = useSWR<{ items: Template[] }, ApiError>(
    editable ? "/v1/panel/offers/templates" : null,
    fetcher,
  );

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {isLoading ? <Skeleton className="h-32 w-full" /> : null}
        {(data?.games ?? []).map((game) => (
          <GameRow
            editable={editable}
            game={game}
            key={game.code}
            onSaved={() => void mutate()}
            templates={templates?.items ?? []}
          />
        ))}
        <QuestionsSection
          editable={editable}
          onChanged={() => void mutate()}
          questions={data?.questions ?? []}
        />
      </CardContent>
    </Card>
  );
}

function GameRow({
  editable,
  game,
  onSaved,
  templates,
}: {
  editable: boolean;
  game: Game;
  onSaved: () => void;
  templates: Template[];
}) {
  const translate = useTranslations("admin.marketing.games");
  const locale = useLocale();
  const { run, pending, error } = useOperatorAction();
  const [form, setForm] = useState(game);
  const [value, setValue] = useState("");
  const ids = {
    enabled: useId(),
    kind: useId(),
    value: useId(),
    template: useId(),
    plays: useId(),
    parameter: useId(),
    cost: useId(),
    rtp: useId(),
    budget: useId(),
    limit: useId(),
  };

  useEffect(() => {
    setForm(game);
    setValue(game.prizeKind === "wallet" ? major(game.prizeValue) : String(game.prizeValue));
  }, [game]);

  async function save() {
    const prizeValue =
      form.prizeKind === "wallet"
        ? minor(value)
        : form.prizeKind === "days"
          ? Number(value) || 0
          : 0;
    if (
      await run(`/v1/panel/marketing/games/${game.code}`, {
        body: { ...form, prizeValue },
        method: "PUT",
        reason: "daily game",
      })
    ) {
      onSaved();
    }
  }

  const number = (key: keyof Game) => (event: React.ChangeEvent<HTMLInputElement>) =>
    setForm({ ...form, [key]: Number(event.target.value) || 0 });

  return (
    <section className="flex flex-col gap-3 rounded-md border p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <Switch
            checked={form.enabled}
            disabled={!editable}
            id={ids.enabled}
            onCheckedChange={(enabled) => setForm({ ...form, enabled })}
          />
          <Label className="font-medium" htmlFor={ids.enabled}>
            {translate(`names.${game.code}`)}
          </Label>
          <Badge variant="outline">
            {translate("win", { percent: game.winPercent.toFixed(2) })}
          </Badge>
          {game.costMinor > 0 ? (
            <Badge variant="outline">
              {translate("rtp", { percent: game.rtpPercent.toFixed(1) })}
            </Badge>
          ) : null}
        </div>
        <span className="text-muted-foreground text-xs">
          {translate("today", {
            paid: formatMoney(game.paidTodayMinor, CURRENCY, locale),
            plays: game.playsToday,
            wins: game.winsToday,
          })}
        </span>
      </div>
      <p className="text-muted-foreground text-xs">{translate(`rules.${game.code}`)}</p>
      <div className="grid gap-3 sm:grid-cols-4">
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.kind}>{translate("prizeKind")}</Label>
          <Select
            disabled={!editable}
            onValueChange={(prizeKind) =>
              setForm({ ...form, prizeKind: prizeKind as Game["prizeKind"] })
            }
            value={form.prizeKind}
          >
            <SelectTrigger id={ids.kind}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(["wallet", "days", "promo"] as const).map((kind) => (
                <SelectItem key={kind} value={kind}>
                  {translate(`prizeKinds.${kind}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        {form.prizeKind === "promo" ? (
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.template}>{translate("template")}</Label>
            <Select
              disabled={!editable}
              onValueChange={(prizeTemplateId) => setForm({ ...form, prizeTemplateId })}
              value={form.prizeTemplateId ?? ""}
            >
              <SelectTrigger id={ids.template}>
                <SelectValue placeholder={translate("pickTemplate")} />
              </SelectTrigger>
              <SelectContent>
                {templates.map((template) => (
                  <SelectItem key={template.id} value={template.id}>
                    {template.titleRu}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        ) : (
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.value}>
              {translate(form.prizeKind === "wallet" ? "prizeAmount" : "prizeDays")}
            </Label>
            <Input
              disabled={!editable}
              id={ids.value}
              inputMode="decimal"
              onChange={(event) => setValue(event.target.value)}
              value={value}
            />
          </div>
        )}
        {PARAMETER_GAMES.includes(game.code) ? (
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.parameter}>{translate(`parameter.${game.code}`)}</Label>
            <Input
              disabled={!editable}
              id={ids.parameter}
              inputMode="numeric"
              onChange={number("parameter")}
              value={String(form.parameter)}
            />
          </div>
        ) : null}
        {ONCE_A_DAY.includes(game.code) ? null : (
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.plays}>{translate("playsPerDay")}</Label>
            <Input
              disabled={!editable}
              id={ids.plays}
              inputMode="numeric"
              onChange={number("playsPerDay")}
              value={String(form.playsPerDay)}
            />
          </div>
        )}
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.cost}>{translate("cost")}</Label>
          <Input
            disabled={!editable}
            id={ids.cost}
            inputMode="decimal"
            onChange={(event) => setForm({ ...form, costMinor: minor(event.target.value) })}
            value={major(form.costMinor)}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.rtp}>{translate("targetRtp")}</Label>
          <Input
            disabled={!editable}
            id={ids.rtp}
            inputMode="numeric"
            onChange={number("targetRtpPercent")}
            value={String(form.targetRtpPercent)}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.budget}>{translate("budget")}</Label>
          <Input
            disabled={!editable}
            id={ids.budget}
            inputMode="decimal"
            onChange={(event) => setForm({ ...form, dailyBudgetMinor: minor(event.target.value) })}
            value={major(form.dailyBudgetMinor)}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.limit}>{translate("limit")}</Label>
          <Input
            disabled={!editable}
            id={ids.limit}
            inputMode="numeric"
            onChange={number("dailyPrizeLimit")}
            value={String(form.dailyPrizeLimit)}
          />
        </div>
      </div>
      {editable ? (
        <Button className="self-start" disabled={pending} onClick={save} size="sm">
          {translate("save")}
        </Button>
      ) : null}
      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error.code === "rtp_exceeded" ? translate("rtpExceeded") : translate("invalid")}
        </p>
      ) : null}
    </section>
  );
}

function QuestionsSection({
  editable,
  onChanged,
  questions,
}: {
  editable: boolean;
  onChanged: () => void;
  questions: Question[];
}) {
  const translate = useTranslations("admin.marketing.games.questions");
  const { run, pending, error } = useOperatorAction();
  const ids = {
    game: useId(),
    ru: useId(),
    en: useId(),
    options: useId(),
    correct: useId(),
    opens: useId(),
  };
  const empty = {
    game: "quiz",
    questionRu: "",
    questionEn: "",
    options: "",
    correct: "1",
    opensAt: "",
  };
  const [form, setForm] = useState(empty);

  async function create() {
    const options = form.options
      .split("\n")
      .map((line) => line.split("|").map((part) => part.trim()))
      .filter((parts) => parts[0])
      .map(([ru, en]) => ({ en: en || ru, ru }));
    if (
      await run("/v1/panel/marketing/games/questions", {
        body: {
          correctIndex: (Number(form.correct) || 1) - 1,
          game: form.game,
          opensAt: form.opensAt ? new Date(form.opensAt).toISOString() : undefined,
          options,
          questionEn: form.questionEn.trim() || form.questionRu.trim(),
          questionRu: form.questionRu.trim(),
        },
        method: "POST",
        reason: "game question",
      })
    ) {
      setForm(empty);
      onChanged();
    }
  }

  async function archive(id: string) {
    if (
      await run(`/v1/panel/marketing/games/questions/${id}/archive`, {
        method: "POST",
        reason: "game question archived",
      })
    ) {
      onChanged();
    }
  }

  return (
    <section className="flex flex-col gap-3">
      <div className="font-medium">{translate("title")}</div>
      <p className="text-muted-foreground text-xs">{translate("description")}</p>
      {questions.length === 0 ? (
        <p className="text-muted-foreground text-sm">{translate("empty")}</p>
      ) : (
        questions.map((question) => (
          <div
            className="flex flex-wrap items-center justify-between gap-2 rounded-md border p-3 text-sm"
            key={question.id}
          >
            <div className="flex min-w-0 flex-col gap-1">
              <span className="font-medium">{question.questionRu}</span>
              <span className="text-muted-foreground text-xs">
                {translate(`games.${question.game}`)} · <Timestamp value={question.opensAt} /> ·{" "}
                {translate("answer", {
                  answer: question.options[question.correctIndex]?.ru ?? "",
                })}{" "}
                · {translate("counts", { answers: question.answers, winners: question.winners })}
              </span>
            </div>
            {question.archivedAt ? (
              <Badge variant="outline">{translate("archived")}</Badge>
            ) : editable ? (
              <Button
                disabled={pending}
                onClick={() => void archive(question.id)}
                size="sm"
                variant="outline"
              >
                {translate("archive")}
              </Button>
            ) : null}
          </div>
        ))
      )}
      {editable ? (
        <div className="grid gap-3 rounded-md border p-3 sm:grid-cols-2">
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.game}>{translate("game")}</Label>
            <Select onValueChange={(game) => setForm({ ...form, game })} value={form.game}>
              <SelectTrigger id={ids.game}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(["quiz", "first_to_answer"] as const).map((game) => (
                  <SelectItem key={game} value={game}>
                    {translate(`games.${game}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.opens}>{translate("opensAt")}</Label>
            <DateTimeField
              hourLabel={translate("hour")}
              id={ids.opens}
              minuteLabel={translate("minute")}
              onChange={(opensAt) => setForm({ ...form, opensAt })}
              placeholder={translate("now")}
              value={form.opensAt}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.ru}>{translate("questionRu")}</Label>
            <Input
              id={ids.ru}
              maxLength={300}
              onChange={(event) => setForm({ ...form, questionRu: event.target.value })}
              value={form.questionRu}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.en}>{translate("questionEn")}</Label>
            <Input
              id={ids.en}
              maxLength={300}
              onChange={(event) => setForm({ ...form, questionEn: event.target.value })}
              value={form.questionEn}
            />
          </div>
          <div className="flex flex-col gap-1 sm:col-span-2">
            <Label htmlFor={ids.options}>{translate("options")}</Label>
            <Textarea
              id={ids.options}
              onChange={(event) => setForm({ ...form, options: event.target.value })}
              placeholder={translate("optionsPlaceholder")}
              rows={4}
              value={form.options}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.correct}>{translate("correct")}</Label>
            <Input
              id={ids.correct}
              inputMode="numeric"
              onChange={(event) => setForm({ ...form, correct: event.target.value })}
              value={form.correct}
            />
          </div>
          <Button
            className="self-end"
            disabled={pending || !form.questionRu.trim() || !form.options.trim()}
            onClick={create}
          >
            {translate("create")}
          </Button>
          {error ? (
            <p className="text-destructive text-sm sm:col-span-2" role="alert">
              {translate("invalid")}
            </p>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}
