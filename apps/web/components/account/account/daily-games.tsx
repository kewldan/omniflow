"use client";

import { Button } from "@omniflow/ui/button";
import { toast } from "@omniflow/ui/toast";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { SectionLabel } from "@/components/account/state";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";
import { useSubmission } from "@/lib/idempotency";

type GameCode =
  | "dice"
  | "slots"
  | "guess_number"
  | "lucky_ticket"
  | "quiz"
  | "daily_streak"
  | "first_to_answer";

type Game = {
  code: GameCode;
  prizeKind: "wallet" | "days" | "promo";
  prizeValue: number;
  prizeTitleRu?: string;
  prizeTitleEn?: string;
  costMinor: number;
  currency: string;
  parameter: number;
  playsLeft: number;
  closed: boolean;
  streak?: number;
  question?: {
    id: string;
    questionRu: string;
    questionEn: string;
    options: { ru: string; en: string }[];
  };
};

type Outcome = {
  won: boolean;
  roll?: number;
  reels?: number[];
  guess?: number;
  secret?: number;
  ticket?: string;
  streak?: number;
};

type Play = { game: GameCode; won: boolean; outcome: Outcome };

const SLOT_SYMBOLS = ["🍒", "🍋", "🔔", "⭐", "🍀", "7️⃣"];
const WITH_PARAMETER: GameCode[] = ["dice", "guess_number", "daily_streak", "first_to_answer"];

/**
 * Daily games. Each draw is made on the server with a cryptographically
 * secure generator, after the customer's choice; the prize is paid in the same
 * transaction as the play. Nothing renders when no game is switched on.
 */
export function DailyGames() {
  const translate = useTranslations("account.account.games");
  const locale = useLocale();
  const money = useMoney();
  const { data, mutate } = useSWR<{ items: Game[] }, ApiError>(
    "/v1/account/engagement/games",
    fetcher,
    {
      shouldRetryOnError: false,
    },
  );
  const submission = useSubmission();
  const [busy, setBusy] = useState<GameCode | null>(null);
  const [results, setResults] = useState<Partial<Record<GameCode, string>>>({});

  const games = data?.items ?? [];
  if (games.length === 0) {
    return null;
  }

  function describe(play: Play): string {
    const outcome = play.outcome;
    let drawn = "";
    switch (play.game) {
      case "dice":
        drawn = translate("result.dice", { roll: outcome.roll ?? 0 });
        break;
      case "slots":
        drawn = (outcome.reels ?? []).map((reel) => SLOT_SYMBOLS[reel] ?? "?").join(" ");
        break;
      case "guess_number":
        drawn = translate("result.guess", {
          guess: outcome.guess ?? 0,
          secret: outcome.secret ?? 0,
        });
        break;
      case "lucky_ticket":
        drawn = translate("result.ticket", { ticket: outcome.ticket ?? "" });
        break;
      case "daily_streak":
        drawn = translate("result.streak", { count: outcome.streak ?? 0 });
        break;
      default:
        drawn = play.won ? translate("result.right") : translate("result.wrong");
    }
    return `${drawn} — ${play.won ? translate("won") : translate("lost")}`;
  }

  function prize(game: Game): string {
    if (game.prizeKind === "wallet") {
      return money(game.prizeValue, game.currency);
    }
    if (game.prizeKind === "days") {
      return translate("days", { count: game.prizeValue });
    }
    return (locale === "ru" ? game.prizeTitleRu : game.prizeTitleEn) ?? "";
  }

  async function play(game: Game, body: { choice?: number; questionId?: string } = {}) {
    setBusy(game.code);
    try {
      const result = await apiFetch<Play>(`/v1/account/engagement/games/${game.code}/play`, {
        body: JSON.stringify(body),
        headers: { "Idempotency-Key": submission.begin() },
        method: "POST",
      });
      submission.settle();
      setResults((previous) => ({ ...previous, [game.code]: describe(result) }));
      if (result.won) {
        toast.success(translate("won"));
      }
      void mutate();
    } catch (failure) {
      submission.settle(failure);
      toast.error((failure as ApiError).message ?? translate("failed"));
      void mutate();
    } finally {
      setBusy(null);
    }
  }

  return (
    <>
      <SectionLabel>{translate("title")}</SectionLabel>
      <p className="text-[12.5px] text-muted-foreground leading-relaxed">{translate("fairness")}</p>
      {games.map((game) => {
        const playable = game.playsLeft > 0 && !game.closed;
        const question = game.question;
        return (
          <section
            className="space-y-2 rounded-xl border border-border bg-card p-4"
            key={game.code}
          >
            <p className="font-semibold text-[14px]">{translate(`name.${game.code}`)}</p>
            <p className="text-[12.5px] text-muted-foreground leading-relaxed">
              {WITH_PARAMETER.includes(game.code)
                ? translate(`rules.${game.code}`, { count: game.parameter })
                : translate(`rules.${game.code}`)}
            </p>
            <p className="text-[13px]">
              {translate("prize", { prize: prize(game) })}
              {game.costMinor > 0 &&
                ` · ${translate("cost", { amount: money(game.costMinor, game.currency) })}`}
              {game.code === "daily_streak" &&
                ` · ${translate("streak", { count: game.streak ?? 0 })}`}
            </p>
            {results[game.code] && (
              <p aria-live="polite" className="font-semibold text-[14px]">
                {results[game.code]}
              </p>
            )}
            {game.closed ? (
              <p className="text-[12.5px] text-muted-foreground">{translate("closed")}</p>
            ) : (game.code === "quiz" || game.code === "first_to_answer") && !question ? (
              <p className="text-[12.5px] text-muted-foreground">{translate("noQuestion")}</p>
            ) : !playable ? (
              <p className="text-[12.5px] text-muted-foreground">{translate("doneToday")}</p>
            ) : null}
            {playable && question && (
              <div className="space-y-2">
                <p className="text-[13.5px]">
                  {locale === "ru" ? question.questionRu : question.questionEn}
                </p>
                <div className="flex flex-col gap-2">
                  {question.options.map((option, index) => (
                    <Button
                      disabled={busy !== null}
                      key={`${question.id}-${option.en}`}
                      onClick={() => play(game, { choice: index, questionId: question.id })}
                      variant="outline"
                    >
                      {locale === "ru" ? option.ru : option.en}
                    </Button>
                  ))}
                </div>
              </div>
            )}
            {playable && game.code === "guess_number" && (
              <div className="grid grid-cols-5 gap-2">
                {Array.from({ length: game.parameter }, (_, index) => index + 1).map((number) => (
                  <Button
                    disabled={busy !== null}
                    key={number}
                    onClick={() => play(game, { choice: number })}
                    variant="outline"
                  >
                    {number}
                  </Button>
                ))}
              </div>
            )}
            {playable &&
              (game.code === "dice" ||
                game.code === "slots" ||
                game.code === "lucky_ticket" ||
                game.code === "daily_streak") && (
                <Button
                  className="w-full"
                  disabled={busy !== null}
                  onClick={() => play(game)}
                  size="lg"
                >
                  {translate(`play.${game.code}`)}
                  {game.code !== "daily_streak" &&
                    ` · ${translate("left", { count: game.playsLeft })}`}
                </Button>
              )}
          </section>
        );
      })}
    </>
  );
}
