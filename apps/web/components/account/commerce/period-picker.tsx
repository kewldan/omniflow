"use client";

import { Button } from "@omniflow/ui/button";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";

import { useMoney } from "@/lib/format";

import type { CheckoutView } from "./types";

const OWN = "own";
const CUSTOM = "custom";

/**
 * The billing period for this checkout: the plan's own, one of its further
 * periods, or a number of days priced by the day.
 *
 * The server prices whatever is chosen and refuses a period the plan does not
 * sell here, so the bounds shown are guidance rather than the check.
 */
export function PeriodPicker({
  checkout,
  disabled,
  onChange,
}: {
  checkout: CheckoutView;
  disabled: boolean;
  onChange: (body: Record<string, unknown>) => void;
}) {
  const translate = useTranslations("account.periodPicker");
  const money = useMoney();
  const selectId = useId();
  const daysId = useId();
  const periods = checkout.periods;
  const chosen = checkout.periodDays ?? 0;
  const listed = periods?.choices.some((choice) => choice.days === chosen) ?? false;
  const [mode, setMode] = useState(chosen === 0 ? OWN : listed ? String(chosen) : CUSTOM);
  const [days, setDays] = useState(chosen > 0 && !listed ? String(chosen) : "");

  if (!periods || (periods.choices.length === 0 && !periods.custom)) {
    return null;
  }
  const custom = periods.custom;

  return (
    <fieldset className="space-y-2 rounded-lg border border-border bg-card p-4">
      <legend className="px-1 font-medium text-sm">{translate("title")}</legend>
      <Label htmlFor={selectId}>{translate("label")}</Label>
      <Select
        disabled={disabled}
        onValueChange={(value) => {
          setMode(value);
          if (value === OWN) {
            onChange({ periodDays: 0 });
          } else if (value !== CUSTOM) {
            onChange({ periodDays: Number(value) });
          }
        }}
        value={mode}
      >
        <SelectTrigger id={selectId}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={OWN}>{translate("own")}</SelectItem>
          {periods.choices.map((choice) => (
            <SelectItem key={choice.days} value={String(choice.days)}>
              {translate("choice", {
                days: choice.days,
                price: money(choice.price.amountMinor, choice.price.currency),
              })}
              {choice.highlighted ? ` · ${translate("best")}` : ""}
            </SelectItem>
          ))}
          {custom && <SelectItem value={CUSTOM}>{translate("custom")}</SelectItem>}
        </SelectContent>
      </Select>
      {mode === CUSTOM && custom && (
        <div className="flex items-end gap-2">
          <div className="flex flex-1 flex-col gap-1.5">
            <Label htmlFor={daysId}>
              {translate("days", { max: custom.maxDays, min: custom.minDays })}
            </Label>
            <Input
              id={daysId}
              inputMode="numeric"
              onChange={(event) => setDays(event.target.value)}
              value={days}
            />
            <span className="text-muted-foreground text-xs">
              {translate("perDay", {
                price: money(custom.pricePerDay.amountMinor, custom.pricePerDay.currency),
              })}
            </span>
          </div>
          <Button
            disabled={disabled || !/^\d+$/.test(days.trim())}
            onClick={() => onChange({ periodDays: Number(days.trim()) })}
            size="sm"
            type="button"
            variant="outline"
          >
            {translate("apply")}
          </Button>
        </div>
      )}
    </fieldset>
  );
}
