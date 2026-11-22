"use client";

import { Button } from "@omniflow/ui/button";
import { Checkbox } from "@omniflow/ui/checkbox";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { useTranslations } from "next-intl";
import { useId } from "react";
import { Controller, useFieldArray, useFormContext } from "react-hook-form";

import { FormField } from "@/components/forms/field";
import { minorToInput, parseMajorToMinor } from "@/lib/money";
import type { PlanVersion } from "@/lib/operations";

/** The period rows and custom days as the form holds them: text, as typed. */
export type PeriodValues = {
  periods: {
    days: string;
    currency: string;
    amount: string;
    forPurchase: boolean;
    forRenewal: boolean;
    highlighted: boolean;
  }[];
  customMin: string;
  customMax: string;
  dayPrices: { currency: string; amount: string }[];
};

/** The form values a version's periods start from. */
export function periodDefaults(current?: PlanVersion): PeriodValues {
  return {
    customMax: current?.customDaysMax ? String(current.customDaysMax) : "",
    customMin: current?.customDaysMin ? String(current.customDaysMin) : "",
    dayPrices: Object.entries(current?.dayPrices ?? {}).map(([currency, amount]) => ({
      amount: minorToInput(amount, currency),
      currency,
    })),
    periods: (current?.periods ?? []).map((period) => ({
      amount: minorToInput(period.amountMinor, period.currency),
      currency: period.currency,
      days: String(period.days),
      forPurchase: period.forPurchase,
      forRenewal: period.forRenewal,
      highlighted: period.highlighted ?? false,
    })),
  };
}

/** Converts the typed rows to the wire shape; the server validates the rest. */
export function readPeriods(values: PeriodValues) {
  const periods = values.periods
    .filter((row) => row.days.trim() !== "")
    .map((row) => {
      const currency = row.currency.trim().toUpperCase();
      const parsed = parseMajorToMinor(row.amount, currency);
      return {
        amountMinor: parsed.ok ? parsed.minor : -1,
        currency,
        days: Number(row.days.trim()),
        forPurchase: row.forPurchase,
        forRenewal: row.forRenewal,
        highlighted: row.highlighted,
      };
    });
  const bounded = values.customMin.trim() !== "" && values.customMax.trim() !== "";
  const dayPrices: Record<string, number> = {};
  if (bounded) {
    for (const row of values.dayPrices) {
      const currency = row.currency.trim().toUpperCase();
      const parsed = parseMajorToMinor(row.amount, currency);
      if (currency && parsed.ok) {
        dayPrices[currency] = parsed.minor;
      }
    }
  }
  return {
    customDaysMax: bounded ? Number(values.customMax.trim()) : null,
    customDaysMin: bounded ? Number(values.customMin.trim()) : null,
    dayPrices: bounded ? dayPrices : {},
    periods,
  };
}

/**
 * Further billing periods for one version, and a number of days the customer
 * chooses, priced by the day — Bedolaga's period prices, renewal periods, and
 * custom days in one place.
 */
export function PeriodsEditor() {
  const translate = useTranslations("admin.catalog.periods");
  const { control, register } = useFormContext<PeriodValues>();
  const periods = useFieldArray({ control, name: "periods" });
  const dayPrices = useFieldArray({ control, name: "dayPrices" });

  return (
    <fieldset className="flex flex-col gap-3 rounded-lg border bg-well border-border p-3">
      <legend className="px-1 font-medium text-sm">{translate("title")}</legend>
      <p className="text-muted-foreground text-xs">{translate("hint")}</p>
      <p className="text-muted-foreground text-xs">{translate("highlightedHint")}</p>
      {periods.fields.map((row, index) => (
        <div
          className="grid items-end gap-2 sm:grid-cols-[1fr_1fr_1fr_auto_auto_auto_auto]"
          key={row.id}
        >
          <FormField label={translate("days")}>
            {(props) => (
              <Input {...props} inputMode="numeric" {...register(`periods.${index}.days`)} />
            )}
          </FormField>
          <FormField label={translate("currency")}>
            {(props) => (
              <Input {...props} maxLength={3} {...register(`periods.${index}.currency`)} />
            )}
          </FormField>
          <FormField label={translate("price")}>
            {(props) => (
              <Input {...props} inputMode="decimal" {...register(`periods.${index}.amount`)} />
            )}
          </FormField>
          <Flag index={index} label={translate("forPurchase")} name="forPurchase" />
          <Flag index={index} label={translate("forRenewal")} name="forRenewal" />
          <Flag index={index} label={translate("highlighted")} name="highlighted" />
          <Button onClick={() => periods.remove(index)} size="sm" type="button" variant="ghost">
            {translate("remove")}
          </Button>
        </div>
      ))}
      <Button
        className="self-start"
        onClick={() =>
          periods.append({
            amount: "",
            currency: "RUB",
            days: "",
            forPurchase: true,
            forRenewal: true,
            highlighted: false,
          })
        }
        size="sm"
        type="button"
        variant="outline"
      >
        {translate("add")}
      </Button>

      <div className="grid gap-2 sm:grid-cols-2">
        <FormField hint={translate("customHint")} label={translate("customMin")}>
          {(props) => <Input {...props} inputMode="numeric" {...register("customMin")} />}
        </FormField>
        <FormField label={translate("customMax")}>
          {(props) => <Input {...props} inputMode="numeric" {...register("customMax")} />}
        </FormField>
      </div>
      {dayPrices.fields.map((row, index) => (
        <div className="grid items-end gap-2 sm:grid-cols-[1fr_1fr_auto]" key={row.id}>
          <FormField label={translate("currency")}>
            {(props) => (
              <Input {...props} maxLength={3} {...register(`dayPrices.${index}.currency`)} />
            )}
          </FormField>
          <FormField label={translate("dayPrice")}>
            {(props) => (
              <Input {...props} inputMode="decimal" {...register(`dayPrices.${index}.amount`)} />
            )}
          </FormField>
          <Button onClick={() => dayPrices.remove(index)} size="sm" type="button" variant="ghost">
            {translate("remove")}
          </Button>
        </div>
      ))}
      <Button
        className="self-start"
        onClick={() => dayPrices.append({ amount: "", currency: "RUB" })}
        size="sm"
        type="button"
        variant="outline"
      >
        {translate("addDayPrice")}
      </Button>
    </fieldset>
  );
}

function Flag({
  index,
  label,
  name,
}: {
  index: number;
  label: string;
  name: "forPurchase" | "forRenewal" | "highlighted";
}) {
  const { control } = useFormContext<PeriodValues>();
  const id = useId();
  return (
    <div className="flex items-center gap-2 pb-2">
      <Controller
        control={control}
        name={`periods.${index}.${name}`}
        render={({ field }) => (
          <Checkbox
            checked={field.value}
            id={id}
            onCheckedChange={(state) => field.onChange(state === true)}
          />
        )}
      />
      <Label htmlFor={id}>{label}</Label>
    </div>
  );
}
