"use client";

import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Switch } from "@omniflow/ui/switch";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";

import type { GoodsProduct } from "@/lib/operations";
import { useOperatorAction } from "@/lib/operations";

const ROUNDING = ["none", "up_minor", "up_unit", "up_ten_units", "up_hundred_units"] as const;

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
  const currencyId = useId();
  const markupId = useId();
  const fixedId = useId();
  const ttlId = useId();

  const costPublished = product.kind === "telegram_stars";
  const pricing = product.pricing;

  const [fixed, setFixed] = useState(Boolean(pricing?.fixedAmountMinor) || !costPublished);
  const [form, setForm] = useState({
    currency: pricing?.currency ?? "RUB",
    fixedAmount: String(pricing?.fixedAmountMinor ?? ""),
    markupBps: String(pricing?.markupBps ?? 0),
    rounding: pricing?.rounding ?? "up_unit",
    ttl: String(pricing?.quoteTtlSeconds ?? 300),
  });
  const { run, pending, error } = useOperatorAction();

  function update(patch: Partial<typeof form>) {
    setForm((current) => ({ ...current, ...patch }));
  }

  return (
    <Card className="flex flex-col gap-3 p-4">
      <p className="font-medium text-sm">
        {translate("pricing.editorTitle", { code: product.code })}
      </p>

      <div className="flex items-center gap-3">
        <Switch
          checked={fixed}
          // Premium has no published cost, so a derived price is not available.
          disabled={!costPublished}
          id={`fixed-${product.id}`}
          onCheckedChange={setFixed}
        />
        <Label htmlFor={`fixed-${product.id}`}>{translate("pricing.useFixed")}</Label>
      </div>
      {!costPublished && (
        <p className="text-muted-foreground text-xs">{translate("pricing.noPublishedCost")}</p>
      )}

      <div className="grid gap-3 sm:grid-cols-2">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={currencyId}>{translate("pricing.currency")}</Label>
          <Input
            id={currencyId}
            maxLength={3}
            onChange={(event) => update({ currency: event.target.value.toUpperCase() })}
            value={form.currency}
          />
        </div>

        {fixed ? (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={fixedId}>{translate("pricing.fixedAmount")}</Label>
            <Input
              id={fixedId}
              inputMode="numeric"
              onChange={(event) => update({ fixedAmount: event.target.value })}
              value={form.fixedAmount}
            />
          </div>
        ) : (
          <>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={markupId}>{translate("pricing.markupBps")}</Label>
              <Input
                id={markupId}
                inputMode="numeric"
                onChange={(event) => update({ markupBps: event.target.value })}
                value={form.markupBps}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={`rounding-${product.id}`}>{translate("pricing.rounding")}</Label>
              <Select onValueChange={(rounding) => update({ rounding })} value={form.rounding}>
                <SelectTrigger id={`rounding-${product.id}`}>
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
            </div>
          </>
        )}

        <div className="flex flex-col gap-1.5">
          <Label htmlFor={ttlId}>{translate("pricing.quoteTtl")}</Label>
          <Input
            id={ttlId}
            inputMode="numeric"
            onChange={(event) => update({ ttl: event.target.value })}
            value={form.ttl}
          />
        </div>
      </div>

      <p className="text-muted-foreground text-xs">{translate("pricing.minorUnitsHint")}</p>
      {error && <p className="text-danger-foreground text-sm">{error.message}</p>}

      <Button
        className="self-start"
        disabled={pending}
        onClick={async () => {
          const ok = await run(`/v1/panel/goods/products/${product.id}/pricing`, {
            body: {
              currency: form.currency,
              fixedAmountMinor: fixed ? Number(form.fixedAmount) : null,
              // A fixed price ignores both, but the API still validates their
              // range, so they are sent as coherent values rather than blanks.
              markupBps: fixed ? 0 : Number(form.markupBps),
              quoteTtlSeconds: Number(form.ttl),
              rounding: fixed ? "none" : form.rounding,
            },
            method: "PUT",
          });
          if (ok) {
            onSaved();
          }
        }}
        size="sm"
      >
        {translate("pricing.save")}
      </Button>
    </Card>
  );
}
