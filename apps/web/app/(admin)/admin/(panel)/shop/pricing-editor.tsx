"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Switch } from "@omniflow/ui/switch";
import { useTranslations } from "next-intl";
import { Controller, useForm, useWatch } from "react-hook-form";
import * as z from "zod/mini";

import { FormField } from "@/components/forms/field";
import { MoneyInput } from "@/components/forms/money-input";
import { currencyCode, moneyIssue, wholeNumber } from "@/lib/form-schema";
import { minorToInput, parseMajorToMinor } from "@/lib/money";
import type { GoodsProduct } from "@/lib/operations";
import { useOperatorAction } from "@/lib/operations";

const ROUNDING = ["none", "up_minor", "up_unit", "up_ten_units", "up_hundred_units"] as const;

/**
 * The form's own shape. Amounts stay text — what the operator typed — and are
 * converted to minor units once, on submission, with the currency's exponent.
 * The bounds are the API's (`internal/panelpg/goods.go`), stated here so the
 * operator is told before the request rather than refused by it.
 */
const schema = z
  .object({
    currency: currencyCode(),
    fixed: z.boolean(),
    fixedAmount: z.string(),
    markupBps: wholeNumber({ max: 100_000, min: 0 }),
    rounding: z.string(),
    ttl: wholeNumber({ max: 3600, min: 30 }),
  })
  .check(
    z.superRefine((values, context) => {
      if (!values.fixed) {
        return;
      }
      const problem = moneyIssue(values.fixedAmount, values.currency.trim().toUpperCase());
      if (problem) {
        context.issues.push({
          code: "custom",
          input: values.fixedAmount,
          message: problem,
          path: ["fixedAmount"],
        });
      }
    }),
  );

type Values = z.infer<typeof schema>;

/**
 * The pricing rule for one product.
 *
 * Two shapes, and the switch between them is the important control. A derived
 * price is the provider's cost plus a markup, rounded up — which needs the
 * provider to publish a cost. A fixed price is what the operator publishes
 * regardless, absorbing the variance themselves.
 *
 * Telegram Premium has no published cost, so it can only be priced fixed. The
 * form says so rather than letting an operator configure a markup over a number
 * that does not exist.
 */
export function PricingEditor({
  onSaved,
  product,
}: {
  onSaved: () => void;
  product: GoodsProduct;
}) {
  const translate = useTranslations("admin.shop");
  const translateForm = useTranslations("common.form");

  const costPublished = product.kind === "telegram_stars";
  const pricing = product.pricing;
  const initialCurrency = pricing?.currency ?? "RUB";

  const form = useForm<Values>({
    defaultValues: {
      currency: initialCurrency,
      fixed: Boolean(pricing?.fixedAmountMinor) || !costPublished,
      // Shown in major units: the stored integer is minor units, which is not
      // how anybody reads a price.
      fixedAmount:
        pricing?.fixedAmountMinor != null
          ? minorToInput(pricing.fixedAmountMinor, initialCurrency)
          : "",
      markupBps: String(pricing?.markupBps ?? 0),
      rounding: pricing?.rounding ?? "up_unit",
      ttl: String(pricing?.quoteTtlSeconds ?? 300),
    },
    resolver: zodResolver(schema),
  });
  const { control, formState, handleSubmit, register } = form;
  const [fixed, currency] = useWatch({ control, name: ["fixed", "currency"] });
  const { run, pending, error } = useOperatorAction();

  async function save(values: Values) {
    const code = values.currency.trim().toUpperCase();
    const amount = parseMajorToMinor(values.fixedAmount, code);
    const ok = await run(`/v1/panel/goods/products/${product.id}/pricing`, {
      body: {
        currency: code,
        fixedAmountMinor: values.fixed && amount.ok ? amount.minor : null,
        // A fixed price ignores both, but the API still validates their
        // range, so they are sent as coherent values rather than blanks.
        markupBps: values.fixed ? 0 : Number(values.markupBps),
        quoteTtlSeconds: Number(values.ttl),
        rounding: values.fixed ? "none" : values.rounding,
      },
      method: "PUT",
    });
    if (ok) {
      onSaved();
    }
  }

  return (
    <Card className="p-4">
      <form className="flex flex-col gap-3" noValidate onSubmit={handleSubmit(save)}>
        <p className="font-medium text-sm">
          {translate("pricing.editorTitle", { code: product.code })}
        </p>

        <div className="flex items-center gap-3">
          <Controller
            control={control}
            name="fixed"
            render={({ field }) => (
              <Switch
                checked={field.value}
                // Premium has no published cost, so a derived price is not available.
                disabled={!costPublished}
                id={`fixed-${product.id}`}
                onCheckedChange={field.onChange}
              />
            )}
          />
          <Label htmlFor={`fixed-${product.id}`}>{translate("pricing.useFixed")}</Label>
        </div>
        {!costPublished && (
          <p className="text-muted-foreground text-xs">{translate("pricing.noPublishedCost")}</p>
        )}

        <div className="grid gap-3 sm:grid-cols-2">
          <FormField
            error={formState.errors.currency?.message}
            label={translate("pricing.currency")}
          >
            {(props) => (
              <Input
                {...props}
                autoCapitalize="characters"
                className="font-mono uppercase"
                maxLength={3}
                {...register("currency")}
              />
            )}
          </FormField>

          {fixed ? (
            <FormField
              error={formState.errors.fixedAmount?.message}
              label={translate("pricing.fixedAmount")}
            >
              {(props) => (
                <MoneyInput {...props} currency={currency} {...register("fixedAmount")} />
              )}
            </FormField>
          ) : (
            <>
              <FormField
                error={formState.errors.markupBps?.message}
                label={translate("pricing.markupBps")}
              >
                {(props) => <Input {...props} inputMode="numeric" {...register("markupBps")} />}
              </FormField>
              <FormField label={translate("pricing.rounding")}>
                {(props) => (
                  <Controller
                    control={control}
                    name="rounding"
                    render={({ field }) => (
                      <Select onValueChange={field.onChange} value={field.value}>
                        <SelectTrigger {...props}>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {ROUNDING.map((mode) => (
                            <SelectItem key={mode} value={mode}>
                              {translate(`rounding.${mode}`)}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    )}
                  />
                )}
              </FormField>
            </>
          )}

          <FormField error={formState.errors.ttl?.message} label={translate("pricing.quoteTtl")}>
            {(props) => <Input {...props} inputMode="numeric" {...register("ttl")} />}
          </FormField>
        </div>

        <p className="text-muted-foreground text-xs">{translate("pricing.minorUnitsHint")}</p>
        {error && (
          <p className="text-danger-foreground text-sm" role="alert">
            {error.message}
          </p>
        )}

        <Button className="self-start" disabled={pending} size="sm" type="submit">
          {pending ? translateForm("saving") : translate("pricing.save")}
        </Button>
      </form>
    </Card>
  );
}
