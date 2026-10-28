"use client";

import { Button } from "@omniflow/ui/button";
import { Input } from "@omniflow/ui/input";
import { Plus, Trash2 } from "lucide-react";
import { useTranslations } from "next-intl";
import { type ReactNode, useId } from "react";
import { useFieldArray, useFormContext, useWatch } from "react-hook-form";

import type { PriceRow } from "@/lib/form-schema";

import { FieldMessage } from "./field";
import { MoneyInput } from "./money-input";

type PriceListValues = { prices: PriceRow[] };

/**
 * A price in each currency the item is sold in, one row per currency.
 *
 * It replaced a free-text `RUB=49900, USD=599` field that took minor units and
 * silently dropped any pair it could not read, so a typo saved a plan with one
 * price fewer and nothing said so. Each row is now its own currency and amount,
 * the amount in major units, and every problem is reported on its row.
 *
 * It reads the form from context: the caller wraps it in `FormProvider` and
 * validates `prices` with `priceList()` from `lib/form-schema.ts`.
 */
export function PriceListField({ hint, label }: { hint?: ReactNode; label: ReactNode }) {
  const translate = useTranslations("common.form.prices");
  const id = useId();
  const {
    control,
    register,
    formState: { errors },
  } = useFormContext<PriceListValues>();
  const { append, fields, remove } = useFieldArray({ control, name: "prices" });
  const rows = useWatch({ control, name: "prices" });

  return (
    <fieldset aria-describedby={hint ? `${id}-hint` : undefined} className="flex flex-col gap-2">
      <legend className="pb-1.5 font-medium text-sm">{label}</legend>
      {fields.map((field, index) => {
        const currency = rows?.[index]?.currency ?? "";
        const rowErrors = errors.prices?.[index];
        return (
          <div className="flex flex-wrap items-start gap-2" key={field.id}>
            <div className="flex w-24 flex-col gap-1">
              <Input
                aria-describedby={rowErrors?.currency ? `${id}-${index}-currency` : undefined}
                aria-invalid={Boolean(rowErrors?.currency)}
                aria-label={translate("currency")}
                autoCapitalize="characters"
                className="font-mono uppercase"
                maxLength={3}
                {...register(`prices.${index}.currency`)}
              />
              <FieldMessage id={`${id}-${index}-currency`} message={rowErrors?.currency?.message} />
            </div>
            <div className="flex min-w-36 flex-1 flex-col gap-1">
              <MoneyInput
                aria-describedby={rowErrors?.amount ? `${id}-${index}-amount` : undefined}
                aria-invalid={Boolean(rowErrors?.amount)}
                aria-label={translate("amount", { currency: currency.toUpperCase() || "—" })}
                currency={currency}
                {...register(`prices.${index}.amount`)}
              />
              <FieldMessage id={`${id}-${index}-amount`} message={rowErrors?.amount?.message} />
            </div>
            <Button
              aria-label={translate("remove", { currency: currency.toUpperCase() || "—" })}
              onClick={() => remove(index)}
              size="icon-sm"
              type="button"
              variant="ghost"
            >
              <Trash2 />
            </Button>
          </div>
        );
      })}
      <Button
        className="self-start"
        onClick={() => append({ amount: "", currency: "" })}
        size="sm"
        type="button"
        variant="outline"
      >
        <Plus />
        {translate("add")}
      </Button>
      {hint && (
        <span className="text-muted-foreground text-xs" id={`${id}-hint`}>
          {hint}
        </span>
      )}
      {/* A list-level problem, such as no rows at all. */}
      <FieldMessage message={errors.prices?.root?.message ?? errors.prices?.message} />
    </fieldset>
  );
}
