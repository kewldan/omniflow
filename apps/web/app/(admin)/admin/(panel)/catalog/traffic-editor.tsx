"use client";

import { Button } from "@omniflow/ui/button";
import { Input } from "@omniflow/ui/input";
import { useTranslations } from "next-intl";
import { useFieldArray, useFormContext } from "react-hook-form";

import { FormField } from "@/components/forms/field";
import { minorToInput, parseMajorToMinor } from "@/lib/money";
import type { PlanVersion } from "@/lib/operations";

/** The custom traffic range as the form holds it: text, as typed. */
export type TrafficValues = {
  trafficMin: string;
  trafficMax: string;
  gbPrices: { currency: string; amount: string }[];
};

/** The form values a version's traffic choice starts from. */
export function trafficDefaults(current?: PlanVersion): TrafficValues {
  return {
    gbPrices: Object.entries(current?.gbPrices ?? {}).map(([currency, amount]) => ({
      amount: minorToInput(amount, currency),
      currency,
    })),
    trafficMax: current?.customTrafficMaxGb ? String(current.customTrafficMaxGb) : "",
    trafficMin: current?.customTrafficMinGb ? String(current.customTrafficMinGb) : "",
  };
}

/** Converts the typed range to the wire shape; the server validates the rest. */
export function readTraffic(values: TrafficValues) {
  const bounded = values.trafficMin.trim() !== "" && values.trafficMax.trim() !== "";
  const gbPrices: Record<string, number> = {};
  if (bounded) {
    for (const row of values.gbPrices) {
      const currency = row.currency.trim().toUpperCase();
      const parsed = parseMajorToMinor(row.amount, currency);
      if (currency && parsed.ok) {
        gbPrices[currency] = parsed.minor;
      }
    }
  }
  return {
    customTrafficMaxGb: bounded ? Number(values.trafficMax.trim()) : null,
    customTrafficMinGb: bounded ? Number(values.trafficMin.trim()) : null,
    gbPrices: bounded ? gbPrices : {},
  };
}

/**
 * A traffic volume the customer chooses at checkout, priced per gigabyte —
 * Bedolaga's custom traffic. The price is for the version's own duration and
 * scales with a longer or shorter period.
 */
export function TrafficEditor() {
  const translate = useTranslations("admin.catalog.customTraffic");
  const { control, register } = useFormContext<TrafficValues>();
  const prices = useFieldArray({ control, name: "gbPrices" });

  return (
    <fieldset className="flex flex-col gap-3 rounded-lg border border-border p-3">
      <legend className="px-1 font-medium text-sm">{translate("title")}</legend>
      <p className="text-muted-foreground text-xs">{translate("hint")}</p>
      <div className="grid gap-2 sm:grid-cols-2">
        <FormField label={translate("min")}>
          {(props) => <Input {...props} inputMode="numeric" {...register("trafficMin")} />}
        </FormField>
        <FormField label={translate("max")}>
          {(props) => <Input {...props} inputMode="numeric" {...register("trafficMax")} />}
        </FormField>
      </div>
      {prices.fields.map((row, index) => (
        <div className="grid items-end gap-2 sm:grid-cols-[1fr_1fr_auto]" key={row.id}>
          <FormField label={translate("currency")}>
            {(props) => (
              <Input {...props} maxLength={3} {...register(`gbPrices.${index}.currency`)} />
            )}
          </FormField>
          <FormField label={translate("price")}>
            {(props) => (
              <Input {...props} inputMode="decimal" {...register(`gbPrices.${index}.amount`)} />
            )}
          </FormField>
          <Button onClick={() => prices.remove(index)} size="sm" type="button" variant="ghost">
            {translate("remove")}
          </Button>
        </div>
      ))}
      <Button
        className="self-start"
        onClick={() => prices.append({ amount: "", currency: "RUB" })}
        size="sm"
        type="button"
        variant="outline"
      >
        {translate("addPrice")}
      </Button>
    </fieldset>
  );
}
