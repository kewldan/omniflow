"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Switch } from "@omniflow/ui/switch";
import { useLocale, useTranslations } from "next-intl";
import { useEffect, useId, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { formatMoney, useOperatorAction } from "@/lib/operations";

type Settings = {
  starsEnabled: boolean;
  starsCurrency: string;
  starsRateMode: "manual" | "auto";
  starsRateMinor: number;
  starsUsdMicros: number;
  usdRubMode: "auto" | "manual";
  usdRubManualMinor?: number;
};

type Currency = {
  settings: Settings;
  usdRub?: { minor: number; source: string; observedAt: string };
  starsRate?: { currency: string; minor: number };
};

/** Parses a decimal major-unit amount into minor units, or undefined. */
function toMinor(value: string): number | undefined {
  const parsed = Number(value.replace(",", "."));
  return Number.isFinite(parsed) && parsed > 0 ? Math.round(parsed * 100) : undefined;
}

/**
 * Exchange rates: what one Telegram Star is worth in rubles, so Stars can pay
 * ruble-priced plans and top-ups, and where the USD/RUB rate comes from.
 */
export function CurrencyCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.payouts.currency");
  const locale = useLocale();
  const { data, mutate } = useSWR<Currency, ApiError>("/v1/panel/finance/currency", fetcher);
  const { run, pending, error } = useOperatorAction();
  const [draft, setDraft] = useState<Settings | null>(null);
  const [rate, setRate] = useState("");
  const [usdManual, setUsdManual] = useState("");
  const ids = {
    enabled: useId(),
    mode: useId(),
    rate: useId(),
    usdMode: useId(),
    usdManual: useId(),
  };

  useEffect(() => {
    if (data && !draft) {
      setDraft(data.settings);
      setRate(String(data.settings.starsRateMinor / 100));
      setUsdManual(
        data.settings.usdRubManualMinor ? String(data.settings.usdRubManualMinor / 100) : "",
      );
    }
  }, [data, draft]);

  async function save() {
    if (!draft) {
      return;
    }
    const ok = await run("/v1/panel/finance/currency", {
      body: {
        ...draft,
        starsRateMinor: toMinor(rate) ?? draft.starsRateMinor,
        usdRubManualMinor: toMinor(usdManual),
      },
      method: "PUT",
      reason: translate("reason"),
    });
    if (ok) {
      setDraft(null);
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
        <p className="text-sm">
          {data?.usdRub
            ? translate("usdNow", {
                rate: formatMoney(data.usdRub.minor, "RUB", locale),
                source: data.usdRub.source,
              })
            : translate("usdUnavailable")}
          {data?.starsRate &&
            ` · ${translate("starNow", { rate: formatMoney(data.starsRate.minor, data.starsRate.currency, locale) })}`}
        </p>
        {draft && (
          <div className="grid gap-3 rounded-md border border-dashed p-4 sm:grid-cols-2">
            <div className="flex items-center gap-2 sm:col-span-2">
              <Switch
                checked={draft.starsEnabled}
                disabled={!editable}
                id={ids.enabled}
                onCheckedChange={(starsEnabled) => setDraft({ ...draft, starsEnabled })}
              />
              <Label htmlFor={ids.enabled}>{translate("starsEnabled")}</Label>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.mode}>{translate("starsMode")}</Label>
              <Select
                disabled={!editable}
                onValueChange={(value) =>
                  setDraft({ ...draft, starsRateMode: value as Settings["starsRateMode"] })
                }
                value={draft.starsRateMode}
              >
                <SelectTrigger id={ids.mode}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="manual">{translate("modes.manual")}</SelectItem>
                  <SelectItem value="auto">{translate("modes.autoStars")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.rate}>{translate("starsRate")}</Label>
              <Input
                disabled={!editable || draft.starsRateMode === "auto"}
                id={ids.rate}
                inputMode="decimal"
                onChange={(event) => setRate(event.target.value)}
                value={rate}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.usdMode}>{translate("usdMode")}</Label>
              <Select
                disabled={!editable}
                onValueChange={(value) =>
                  setDraft({ ...draft, usdRubMode: value as Settings["usdRubMode"] })
                }
                value={draft.usdRubMode}
              >
                <SelectTrigger id={ids.usdMode}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="auto">{translate("modes.autoUsd")}</SelectItem>
                  <SelectItem value="manual">{translate("modes.manual")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.usdManual}>{translate("usdManual")}</Label>
              <Input
                disabled={!editable}
                id={ids.usdManual}
                inputMode="decimal"
                onChange={(event) => setUsdManual(event.target.value)}
                value={usdManual}
              />
            </div>
            {editable && (
              <div className="flex items-center gap-2 sm:col-span-2">
                <Button disabled={pending} onClick={save} type="button">
                  {translate("save")}
                </Button>
                {error && <span className="text-danger-foreground text-xs">{error.message}</span>}
              </div>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
