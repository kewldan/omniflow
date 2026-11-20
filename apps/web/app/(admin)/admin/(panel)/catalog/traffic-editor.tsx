"use client";

import { Button } from "@omniflow/ui/button";
import { Input } from "@omniflow/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { useTranslations } from "next-intl";
import { Controller, useFieldArray, useFormContext, useWatch } from "react-hook-form";

import { FormField } from "@/components/forms/field";
import { minorToInput, parseMajorToMinor } from "@/lib/money";
import type { PlanVersion } from "@/lib/operations";

/** Exactly what the `plan_versions` CHECK on `traffic_mode` accepts. */
const TRAFFIC_MODES = ["fixed_with_topup", "fixed", "selectable"] as const;

/** The traffic choice as the form holds it: text, as typed. */
export type TrafficValues = {
  trafficMode: string;
  trafficPackages: { gb: string; currency: string; amount: string }[];
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
    trafficMode: current?.trafficMode ?? "fixed_with_topup",
    trafficPackages: (current?.trafficPackages ?? []).map((pack) => ({
      amount: minorToInput(pack.amountMinor, pack.currency),
      currency: pack.currency,
      gb: String(pack.gb),
    })),
  };
}

/**
 * Converts the typed choice to the wire shape; the server validates the rest.
 * Only a selectable version sends packages or a range, so switching the mode
 * away from it publishes neither rather than a version the server refuses.
 */
export function readTraffic(values: TrafficValues) {
  const selectable = values.trafficMode === "selectable";
  const bounded = selectable && values.trafficMin.trim() !== "" && values.trafficMax.trim() !== "";
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
  const trafficPackages = selectable
    ? values.trafficPackages
        .filter((row) => row.gb.trim() !== "")
        .map((row) => {
          const currency = row.currency.trim().toUpperCase();
          const parsed = parseMajorToMinor(row.amount, currency);
          return {
            amountMinor: parsed.ok ? parsed.minor : -1,
            currency,
            gb: Number(row.gb.trim()),
          };
        })
    : [];
  return {
    customTrafficMaxGb: bounded ? Number(values.trafficMax.trim()) : null,
    customTrafficMinGb: bounded ? Number(values.trafficMin.trim()) : null,
    gbPrices: bounded ? gbPrices : {},
    trafficMode: values.trafficMode,
    trafficPackages,
  };
}

/**
 * How the version sells traffic — Bedolaga's TRAFFIC_SELECTION_MODE — and,
 * for a selectable version, the packages and the custom volume the customer
 * chooses from. Prices are for this version's own duration and scale with a
 * longer or shorter period.
 */
export function TrafficEditor() {
  const translate = useTranslations("admin.catalog.customTraffic");
  const { control, register } = useFormContext<TrafficValues>();
  const mode = useWatch({ control, name: "trafficMode" });
  const packages = useFieldArray({ control, name: "trafficPackages" });
  const prices = useFieldArray({ control, name: "gbPrices" });

  return (
    <fieldset className="flex flex-col gap-3 rounded-lg border border-border p-3">
      <legend className="px-1 font-medium text-sm">{translate("title")}</legend>
      <FormField hint={translate(`modeHint.${mode}`)} label={translate("mode")}>
        {(props) => (
          <Controller
            control={control}
            name="trafficMode"
            render={({ field }) => (
              <Select onValueChange={field.onChange} value={field.value}>
                <SelectTrigger {...props}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {TRAFFIC_MODES.map((option) => (
                    <SelectItem key={option} value={option}>
                      {translate(`modeValue.${option}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          />
        )}
      </FormField>
      {mode === "selectable" && (
        <>
          <p className="text-muted-foreground text-xs">{translate("packagesHint")}</p>
          {packages.fields.map((row, index) => (
            <div className="grid items-end gap-2 sm:grid-cols-[1fr_1fr_1fr_auto]" key={row.id}>
              <FormField label={translate("packageGb")}>
                {(props) => (
                  <Input
                    {...props}
                    inputMode="numeric"
                    {...register(`trafficPackages.${index}.gb`)}
                  />
                )}
              </FormField>
              <FormField label={translate("currency")}>
                {(props) => (
                  <Input
                    {...props}
                    maxLength={3}
                    {...register(`trafficPackages.${index}.currency`)}
                  />
                )}
              </FormField>
              <FormField label={translate("packagePrice")}>
                {(props) => (
                  <Input
                    {...props}
                    inputMode="decimal"
                    {...register(`trafficPackages.${index}.amount`)}
                  />
                )}
              </FormField>
              <Button
                onClick={() => packages.remove(index)}
                size="sm"
                type="button"
                variant="ghost"
              >
                {translate("remove")}
              </Button>
            </div>
          ))}
          <Button
            className="self-start"
            onClick={() => packages.append({ amount: "", currency: "RUB", gb: "" })}
            size="sm"
            type="button"
            variant="outline"
          >
            {translate("addPackage")}
          </Button>

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
        </>
      )}
    </fieldset>
  );
}
