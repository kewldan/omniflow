"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import { type Control, Controller, FormProvider, useForm, useWatch } from "react-hook-form";
import useSWR from "swr";
import * as z from "zod/mini";

import { StateNotice } from "@/components/admin/state-notice";
import { FormField } from "@/components/forms/field";
import { PriceListField } from "@/components/forms/price-list-field";
import { type ApiError, fetcher } from "@/lib/api";
import { issue, priceList, readPriceList, requiredText, wholeNumber } from "@/lib/form-schema";
import { type Listing, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

type Addon = {
  id: string;
  code: string;
  kind: string;
  visible: boolean;
  sortOrder: number;
  latestVersion: number;
  archivedAt?: string;
};

const KINDS = ["traffic", "devices", "squads"] as const;
const PRORATIONS = ["full_price", "remaining_period", "daily_rate"] as const;

/**
 * The add-on catalogue.
 *
 * An add-on splits the same way a plan does: the add-on row carries
 * presentation and is mutable, while the version carries what the customer gets
 * and what it costs, and is not. Saving publishes a new version, so a
 * historical order keeps costing what it cost.
 */
export function Addons({ active }: { active: boolean }) {
  const translate = useTranslations("admin.catalog.addons");
  const { can } = useSession();
  const [composing, setComposing] = useState(false);

  const { data, isLoading, mutate } = useSWR<Listing<Addon>, ApiError>(
    active ? "/v1/panel/catalog/addons" : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];

  return (
    <div className="flex flex-col gap-4">
      {can("catalog.write") && (
        <Button className="self-start" onClick={() => setComposing((open) => !open)} size="sm">
          {translate(composing ? "close" : "create")}
        </Button>
      )}
      {composing && (
        <AddonComposer
          onSaved={() => {
            setComposing(false);
            void mutate();
          }}
        />
      )}
      {items.length === 0 ? (
        <StateNotice
          description={translate("empty.description")}
          title={translate("empty.title")}
          variant="empty"
        />
      ) : (
        <Card className="flex flex-col gap-2 p-4">
          {items.map((addon) => (
            <div className="flex flex-wrap items-center justify-between gap-3" key={addon.id}>
              <span className="flex items-center gap-2">
                <span className="font-mono text-[12px]">{addon.code}</span>
                <Badge variant="neutral">{translate(`kind.${addon.kind}`)}</Badge>
                <span className="text-muted-foreground text-xs">v{addon.latestVersion}</span>
              </span>
              <Badge variant={addon.visible ? "success" : "neutral"}>
                {translate(addon.visible ? "visible" : "hidden")}
              </Badge>
            </div>
          ))}
        </Card>
      )}
    </div>
  );
}

const composerSchema = z
  .object({
    code: requiredText(64),
    kind: z.string(),
    maxQuantity: wholeNumber({ max: 1000, min: 1 }),
    nameEn: requiredText(),
    nameRu: requiredText(),
    payload: z.string(),
    prices: priceList(),
    proration: z.string(),
    reason: requiredText(),
    visible: z.boolean(),
  })
  .check(
    // What the add-on grants depends on its kind, so the one payload field is
    // checked against the kind it is for.
    z.superRefine((values, context) => {
      const payload = values.payload.trim().replace(",", ".");
      let problem: string | null = null;
      if (values.kind === "traffic") {
        problem =
          payload === ""
            ? issue("required")
            : !/^\d+(\.\d+)?$/.test(payload)
              ? issue("number")
              : Number(payload) <= 0
                ? issue("min", { min: 0.1 })
                : null;
      } else if (values.kind === "devices") {
        problem =
          payload === ""
            ? issue("required")
            : !/^\d+$/.test(payload)
              ? issue("integer")
              : Number(payload) < 1
                ? issue("min", { min: 1 })
                : null;
      } else if (splitList(values.payload).length === 0) {
        problem = issue("required");
      }
      if (problem) {
        context.issues.push({
          code: "custom",
          input: values.payload,
          message: problem,
          path: ["payload"],
        });
      }
    }),
  );

type ComposerValues = z.infer<typeof composerSchema>;

/**
 * Creates an add-on and publishes a version of it.
 *
 * Proration is the interesting field. `full_price` charges the whole amount
 * whatever is left of the period, `remaining_period` scales it to what remains,
 * and `daily_rate` prices it by day — three answers to "what should a mid-period
 * purchase cost?", none of which is obviously right for every operator.
 */
function AddonComposer({ onSaved }: { onSaved: () => void }) {
  const translate = useTranslations("admin.catalog.addons");
  const translateForm = useTranslations("common.form");
  const visibleId = useId();

  const form = useForm<ComposerValues>({
    defaultValues: {
      code: "",
      kind: "traffic",
      maxQuantity: "1",
      nameEn: "",
      nameRu: "",
      payload: "",
      prices: [{ amount: "", currency: "RUB" }],
      proration: "remaining_period",
      reason: "",
      visible: false,
    },
    resolver: zodResolver(composerSchema),
  });
  const { control, formState, handleSubmit, register, resetField } = form;
  const errors = formState.errors;
  const kind = useWatch({ control, name: "kind" });
  const { run, pending, error } = useOperatorAction();

  async function save(values: ComposerValues) {
    const payload = values.payload.trim().replace(",", ".");
    const ok = await run("/v1/panel/catalog/addons", {
      body: {
        code: values.code.trim().toLowerCase(),
        descriptionEn: "",
        descriptionRu: "",
        deviceSlots: values.kind === "devices" ? Number(payload) : null,
        kind: values.kind,
        maxQuantity: Number(values.maxQuantity),
        nameEn: values.nameEn.trim(),
        nameRu: values.nameRu.trim(),
        prices: readPriceList(values.prices),
        proration: values.proration,
        sortOrder: 0,
        squadIds: values.kind === "squads" ? splitList(values.payload) : [],
        // Traffic is entered in gigabytes and stored in bytes, because every
        // allowance in the product is bytes.
        trafficBytes:
          values.kind === "traffic" ? Math.round(Number(payload) * 1_073_741_824) : null,
        visible: values.visible,
      },
      method: "PUT",
      reason: values.reason.trim(),
    });
    if (ok) {
      resetField("reason");
      onSaved();
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("composer.title")}</CardTitle>
        <CardDescription>{translate("composer.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        <FormProvider {...form}>
          <form className="flex flex-col gap-3" noValidate onSubmit={handleSubmit(save)}>
            <div className="grid gap-3 sm:grid-cols-2">
              <FormField error={errors.code?.message} label={translate("composer.code")}>
                {(props) => <Input {...props} {...register("code")} />}
              </FormField>
              <ClosedList
                control={control}
                label={translate("composer.kind")}
                name="kind"
                options={KINDS.map((option) => [option, translate(`kind.${option}`)])}
              />
              <FormField error={errors.nameEn?.message} label={translate("composer.nameEn")}>
                {(props) => <Input {...props} {...register("nameEn")} />}
              </FormField>
              <FormField error={errors.nameRu?.message} label={translate("composer.nameRu")}>
                {(props) => <Input {...props} {...register("nameRu")} />}
              </FormField>
            </div>

            <FormField
              error={errors.payload?.message}
              hint={translate(`composer.payloadHint.${kind}`)}
              label={translate(`composer.payload.${kind}`)}
            >
              {(props) => (
                <Input
                  {...props}
                  inputMode={
                    kind === "squads" ? "text" : kind === "devices" ? "numeric" : "decimal"
                  }
                  {...register("payload")}
                />
              )}
            </FormField>

            <div className="grid gap-3 sm:grid-cols-2">
              <FormField
                error={errors.maxQuantity?.message}
                hint={translate("composer.quantityHint")}
                label={translate("composer.quantity")}
              >
                {(props) => <Input {...props} inputMode="numeric" {...register("maxQuantity")} />}
              </FormField>
              <ClosedList
                control={control}
                label={translate("composer.proration")}
                name="proration"
                options={PRORATIONS.map((option) => [option, translate(`proration.${option}`)])}
              />
            </div>

            <PriceListField
              hint={translate("composer.pricesHint")}
              label={translate("composer.prices")}
            />
            <div className="flex items-center gap-3">
              <Controller
                control={control}
                name="visible"
                render={({ field }) => (
                  <Switch checked={field.value} id={visibleId} onCheckedChange={field.onChange} />
                )}
              />
              <Label htmlFor={visibleId}>{translate("composer.visible")}</Label>
            </div>
            <FormField error={errors.reason?.message} label={translate("composer.reason")}>
              {(props) => <Input {...props} {...register("reason")} />}
            </FormField>

            <p className="text-muted-foreground text-xs">{translate("composer.immutability")}</p>
            {error && (
              <p className="text-danger-foreground text-sm" role="alert">
                {error.message}
              </p>
            )}
            <Button className="self-start" disabled={pending} size="sm" type="submit">
              {pending ? translateForm("saving") : translate("composer.submit")}
            </Button>
          </form>
        </FormProvider>
      </CardContent>
    </Card>
  );
}

/** A closed list, as the shared `Select` rather than the browser's own control. */
function ClosedList({
  control,
  label,
  name,
  options,
}: {
  control: Control<ComposerValues>;
  label: string;
  name: "kind" | "proration";
  options: [string, string][];
}) {
  return (
    <FormField label={label}>
      {(props) => (
        <Controller
          control={control}
          name={name}
          render={({ field }) => (
            <Select onValueChange={field.onChange} value={field.value}>
              <SelectTrigger {...props}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {options.map(([value, text]) => (
                  <SelectItem key={value} value={value}>
                    {text}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        />
      )}
    </FormField>
  );
}

function splitList(input: string): string[] {
  return input
    .split(",")
    .map((value) => value.trim())
    .filter(Boolean);
}
