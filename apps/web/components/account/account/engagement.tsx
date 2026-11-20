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

type Prize = {
  labelRu: string;
  labelEn: string;
  kind: "wallet" | "days" | "nothing";
  value: number;
};
type Wheel = {
  enabled: boolean;
  costMinor: number;
  currency: string;
  freeLeft: number;
  spinsLeft: number;
  prizes: Prize[];
};
type Poll = {
  id: string;
  questionRu: string;
  questionEn: string;
  options: { ru: string; en: string }[];
  rewardKind: "none" | "wallet" | "days";
  rewardValue: number;
  answered?: number | null;
};

/**
 * The fortune wheel and open polls. Each renders nothing when the operator
 * has not switched it on, so the screen stays the same for installations
 * that use neither.
 */
export function Engagement() {
  const translate = useTranslations("account.account.engagement");
  const locale = useLocale();
  const money = useMoney();
  const { data: wheel, mutate: refreshWheel } = useSWR<Wheel, ApiError>(
    "/v1/account/engagement/wheel",
    fetcher,
    {
      shouldRetryOnError: false,
    },
  );
  const { data: polls, mutate: refreshPolls } = useSWR<{ items: Poll[] }, ApiError>(
    "/v1/account/engagement/polls",
    fetcher,
    { shouldRetryOnError: false },
  );
  const spin = useSubmission();
  const [busy, setBusy] = useState(false);
  const [won, setWon] = useState<string | null>(null);
  const label = (prize: Prize) => (locale === "ru" ? prize.labelRu : prize.labelEn);

  async function spinWheel() {
    setBusy(true);
    try {
      const result = await apiFetch<{ prize: Prize }>("/v1/account/engagement/wheel/spin", {
        headers: { "Idempotency-Key": spin.begin() },
        method: "POST",
      });
      spin.settle();
      setWon(
        result.prize.kind === "nothing"
          ? translate("nothing")
          : translate("won", { prize: label(result.prize) }),
      );
      void refreshWheel();
    } catch (error) {
      spin.settle(error);
      toast.error((error as ApiError).message ?? translate("failed"));
    } finally {
      setBusy(false);
    }
  }

  async function answer(poll: Poll, option: number) {
    setBusy(true);
    try {
      const result = await apiFetch<{ rewarded: boolean }>(
        `/v1/account/engagement/polls/${poll.id}/answer`,
        {
          body: JSON.stringify({ option }),
          method: "POST",
        },
      );
      toast.success(result.rewarded ? translate("thanksRewarded") : translate("thanks"));
      void refreshPolls();
    } catch (error) {
      toast.error((error as ApiError).message ?? translate("failed"));
    } finally {
      setBusy(false);
    }
  }

  const open = polls?.items ?? [];
  if (!wheel?.enabled && open.length === 0) {
    return null;
  }
  return (
    <>
      {wheel?.enabled && (
        <>
          <SectionLabel>{translate("wheelTitle")}</SectionLabel>
          <section className="space-y-3 rounded-xl border border-border bg-card p-4">
            <p className="text-[13px] text-muted-foreground">
              {wheel.prizes.map(label).join(" · ")}
            </p>
            <p className="text-[13px]">
              {wheel.freeLeft > 0
                ? translate("free", { count: wheel.freeLeft })
                : translate("cost", {
                    amount: money(wheel.costMinor, wheel.currency),
                    count: wheel.spinsLeft,
                  })}
            </p>
            {won && (
              <p aria-live="polite" className="font-semibold text-[14px]">
                {won}
              </p>
            )}
            <Button
              className="w-full"
              disabled={busy || wheel.spinsLeft === 0}
              onClick={spinWheel}
              size="lg"
            >
              {wheel.spinsLeft === 0 ? translate("noneLeft") : translate("spin")}
            </Button>
          </section>
        </>
      )}
      {open.length > 0 && (
        <>
          <SectionLabel>{translate("pollsTitle")}</SectionLabel>
          {open.map((poll) => (
            <section
              className="space-y-2 rounded-xl border border-border bg-card p-4"
              key={poll.id}
            >
              <p className="font-semibold text-[14px]">
                {locale === "ru" ? poll.questionRu : poll.questionEn}
              </p>
              {poll.rewardKind !== "none" && poll.answered == null && (
                <p className="text-[12.5px] text-muted-foreground">
                  {poll.rewardKind === "wallet"
                    ? translate("rewardWallet", {
                        amount: money(poll.rewardValue, wheel?.currency ?? "RUB"),
                      })
                    : translate("rewardDays", { count: poll.rewardValue })}
                </p>
              )}
              <div className="flex flex-col gap-2">
                {poll.options.map((option, index) => (
                  <Button
                    disabled={busy || poll.answered != null}
                    key={option.en}
                    onClick={() => answer(poll, index)}
                    variant={poll.answered === index ? "default" : "outline"}
                  >
                    {locale === "ru" ? option.ru : option.en}
                  </Button>
                ))}
              </div>
            </section>
          ))}
        </>
      )}
    </>
  );
}
