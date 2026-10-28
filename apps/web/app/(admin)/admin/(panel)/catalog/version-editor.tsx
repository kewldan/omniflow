"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { useTranslations } from "next-intl";
import { useId } from "react";
import { type Control, Controller, FormProvider, useForm, useWatch } from "react-hook-form";
import useSWR from "swr";
import * as z from "zod/mini";

import { FormField } from "@/components/forms/field";
import { PriceListField } from "@/components/forms/price-list-field";
import { type ApiError, fetcher } from "@/lib/api";
import {
  decimalNumber,
  priceList,
  readDecimal,
  readPriceList,
  readWholeNumber,
  requiredText,
  wholeNumber,
} from "@/lib/form-schema";
import { minorToInput } from "@/lib/money";
import type { PlanVersion } from "@/lib/operations";
import { useOperatorAction } from "@/lib/operations";

const BILLING_PERIODS = ["day", "week", "month", "quarter", "year", "custom"] as const;
const SQUAD_SELECTIONS = ["automatic", "customer_choice", "fixed"] as const;
const UPGRADE_POLICIES = ["forbid", "replace", "extend"] as const;
const DOWNGRADE_POLICIES = ["forbid", "immediate", "at_expiry"] as const;
const CANCELLATION_POLICIES = ["immediate", "at_expiry"] as const;
const TRIAL_ELIGIBILITY = ["new_customer", "never", "any_customer"] as const;

const GIBIBYTE = 1_073_741_824;

const schema = z.object({
  billingPeriod: z.string(),
  cancellationPolicy: z.string(),
  // Empty means unlimited, which is a different thing from zero — zero would
  // be a plan that permits nothing.
  deviceLimit: wholeNumber({ min: 1, optional: true }),
  downgradePolicy: z.string(),
  durationDays: wholeNumber({ min: 1 }),
  graceDays: wholeNumber({ min: 0, optional: true }),
  maxSquads: wholeNumber({ min: 1, optional: true }),
  minSquads: wholeNumber({ min: 0, optional: true }),
  prices: priceList(),
  reason: requiredText(),
  recurringCapable: z.boolean(),
  squadIds: z.string(),
  squadSelection: z.string(),
  trafficGb: decimalNumber({ min: 0, optional: true }),
  trialEligibility: z.string(),
  upgradePolicy: z.string(),
});

type Values = z.infer<typeof schema>;

/**
 * Publishes the next version of a plan, starting from the current one.
 *
 * The form was always written to pre-fill from `current`, and nothing ever
 * passed it: the caller rendered the editor with a plan id alone, so every
 * "publish the next version" began as an empty form. Changing a price then meant
 * retyping the duration, the traffic cap, the device limit, the squads, and the
 * policies from memory — and the panel showed none of them anywhere else, so
 * "from memory" was literal.
 *
 * The plan detail is fetched here rather than by the caller because this is the
 * only screen that needs it, and the form is keyed on the version it starts from
 * so the fields initialise once the answer is known instead of mounting empty
 * and being reset underneath the operator.
 */
export function PlanVersionEditor({
  current,
  onPublished,
  planId,
}: {
  current?: PlanVersion;
  onPublished: () => void;
  planId: string;
}) {
  const { data, isLoading } = useSWR<{ versions: PlanVersion[] }, ApiError>(
    current ? null : `/v1/panel/catalog/plans/${planId}`,
    fetcher,
  );

  if (current) {
    return <VersionForm current={current} onPublished={onPublished} planId={planId} />;
  }
  if (isLoading) {
    return <Skeleton className="h-96 w-full" />;
  }

  // The newest version that has not been retired is what the next one continues
  // from. A retired version is one the operator has already withdrawn, and
  // starting from it would re-publish something they took down on purpose.
  const live = (data?.versions ?? [])
    .filter((version) => !version.retiredAt)
    .sort((left, right) => right.version - left.version)[0];

  return (
    <VersionForm current={live} key={live?.id ?? "new"} onPublished={onPublished} planId={planId} />
  );
}

function defaults(current?: PlanVersion): Values {
  return {
    billingPeriod: current?.billingPeriod ?? "month",
    cancellationPolicy: current?.cancellationPolicy ?? "at_expiry",
    deviceLimit: current?.deviceLimit ? String(current.deviceLimit) : "",
    downgradePolicy: current?.downgradePolicy ?? "at_expiry",
    durationDays: String(Math.round((current?.durationSeconds ?? 2_592_000) / 86_400)),
    graceDays: String(Math.round((current?.gracePeriodSeconds ?? 0) / 86_400)),
    maxSquads: current?.maxSelectableSquads ? String(current.maxSelectableSquads) : "",
    minSquads: String(current?.minSelectableSquads ?? 0),
    // Shown in major units, as a customer reads them; stored as minor units.
    prices: Object.entries(current?.prices ?? {}).map(([currency, amount]) => ({
      amount: minorToInput(amount, currency),
      currency,
    })),
    reason: "",
    recurringCapable: current?.recurringCapable ?? false,
    squadIds: (current?.squadIds ?? []).join(", "),
    squadSelection: current?.squadSelection ?? "automatic",
    trafficGb: current?.trafficAllowanceBytes
      ? String(current.trafficAllowanceBytes / GIBIBYTE)
      : "",
    trialEligibility: current?.trialEligibility ?? "new_customer",
    upgradePolicy: current?.upgradePolicy ?? "extend",
  };
}

function VersionForm({
  current,
  onPublished,
  planId,
}: {
  current?: PlanVersion;
  onPublished: () => void;
  planId: string;
}) {
  const translate = useTranslations("admin.catalog.version");
  const translateForm = useTranslations("common.form");
  const recurringId = useId();

  const form = useForm<Values>({ defaultValues: defaults(current), resolver: zodResolver(schema) });
  const { control, formState, handleSubmit, register, resetField } = form;
  const squadSelection = useWatch({ control, name: "squadSelection" });
  const { run, pending, error } = useOperatorAction();
  const errors = formState.errors;

  async function publish(values: Values) {
    const ok = await run(`/v1/panel/catalog/plans/${planId}/versions`, {
      body: {
        billingPeriod: values.billingPeriod,
        cancellationPolicy: values.cancellationPolicy,
        deviceLimit: readWholeNumber(values.deviceLimit),
        downgradePolicy: values.downgradePolicy,
        durationSeconds: Number(values.durationDays) * 86_400,
        gracePeriodSeconds: (readWholeNumber(values.graceDays) ?? 0) * 86_400,
        maxSelectableSquads: readWholeNumber(values.maxSquads),
        minSelectableSquads: readWholeNumber(values.minSquads) ?? 0,
        prices: readPriceList(values.prices),
        recurringCapable: values.recurringCapable,
        squadIds: splitList(values.squadIds),
        squadSelection: values.squadSelection,
        trafficAllowanceBytes: (() => {
          const gigabytes = readDecimal(values.trafficGb);
          return gigabytes === null ? null : Math.round(gigabytes * GIBIBYTE);
        })(),
        trialEligibility: values.trialEligibility,
        upgradePolicy: values.upgradePolicy,
      },
      method: "POST",
      reason: values.reason.trim(),
    });
    if (ok) {
      resetField("reason");
      onPublished();
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent>
        <FormProvider {...form}>
          <form className="flex flex-col gap-4" noValidate onSubmit={handleSubmit(publish)}>
            <div className="grid gap-3 sm:grid-cols-3">
              <Choice
                control={control}
                label={translate("billingPeriod")}
                name="billingPeriod"
                options={BILLING_PERIODS}
                prefix="billingPeriod"
              />
              <FormField
                error={errors.durationDays?.message}
                hint={translate("durationHint")}
                label={translate("duration")}
              >
                {(props) => <Input {...props} inputMode="numeric" {...register("durationDays")} />}
              </FormField>
              <FormField
                error={errors.graceDays?.message}
                hint={translate("graceHint")}
                label={translate("grace")}
              >
                {(props) => <Input {...props} inputMode="numeric" {...register("graceDays")} />}
              </FormField>
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <FormField
                error={errors.trafficGb?.message}
                hint={translate("trafficHint")}
                label={translate("traffic")}
              >
                {(props) => <Input {...props} inputMode="decimal" {...register("trafficGb")} />}
              </FormField>
              <FormField
                error={errors.deviceLimit?.message}
                hint={translate("devicesHint")}
                label={translate("devices")}
              >
                {(props) => <Input {...props} inputMode="numeric" {...register("deviceLimit")} />}
              </FormField>
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <FormField hint={translate("squadsHint")} label={translate("squads")}>
                {(props) => <Input {...props} {...register("squadIds")} />}
              </FormField>
              <Choice
                control={control}
                label={translate("squadSelection")}
                name="squadSelection"
                options={SQUAD_SELECTIONS}
                prefix="squadSelection"
              />
              {squadSelection === "customer_choice" && (
                <>
                  <FormField error={errors.minSquads?.message} label={translate("minSquads")}>
                    {(props) => <Input {...props} inputMode="numeric" {...register("minSquads")} />}
                  </FormField>
                  <FormField
                    error={errors.maxSquads?.message}
                    hint={translate("maxSquadsHint")}
                    label={translate("maxSquads")}
                  >
                    {(props) => <Input {...props} inputMode="numeric" {...register("maxSquads")} />}
                  </FormField>
                </>
              )}
            </div>

            <div className="grid gap-3 sm:grid-cols-3">
              <Choice
                control={control}
                label={translate("upgradePolicy")}
                name="upgradePolicy"
                options={UPGRADE_POLICIES}
                prefix="upgradePolicy"
              />
              <Choice
                control={control}
                label={translate("downgradePolicy")}
                name="downgradePolicy"
                options={DOWNGRADE_POLICIES}
                prefix="downgradePolicy"
              />
              <Choice
                control={control}
                label={translate("cancellationPolicy")}
                name="cancellationPolicy"
                options={CANCELLATION_POLICIES}
                prefix="cancellationPolicy"
              />
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <Choice
                control={control}
                label={translate("trialEligibility")}
                name="trialEligibility"
                options={TRIAL_ELIGIBILITY}
                prefix="trialEligibility"
              />
              <div className="flex items-center gap-3 pt-6">
                <Controller
                  control={control}
                  name="recurringCapable"
                  render={({ field }) => (
                    <Switch
                      checked={field.value}
                      id={recurringId}
                      onCheckedChange={field.onChange}
                    />
                  )}
                />
                <Label htmlFor={recurringId}>{translate("recurringCapable")}</Label>
              </div>
            </div>

            <PriceListField hint={translate("pricesHint")} label={translate("prices")} />

            <FormField
              error={errors.reason?.message}
              hint={translate("reasonHint")}
              label={translate("reason")}
            >
              {(props) => <Input {...props} {...register("reason")} />}
            </FormField>

            <p className="text-muted-foreground text-xs">{translate("immutability")}</p>
            {error && (
              <p className="text-danger-foreground text-sm" role="alert">
                {error.message}
              </p>
            )}
            <Button className="self-start" disabled={pending} size="sm" type="submit">
              {pending ? translateForm("saving") : translate("publish")}
            </Button>
          </form>
        </FormProvider>
      </CardContent>
    </Card>
  );
}

type ChoiceField =
  | "billingPeriod"
  | "squadSelection"
  | "upgradePolicy"
  | "downgradePolicy"
  | "cancellationPolicy"
  | "trialEligibility";

/** A closed list, as the shared `Select` rather than the browser's own control. */
function Choice({
  control,
  label,
  name,
  options,
  prefix,
}: {
  control: Control<Values>;
  label: string;
  name: ChoiceField;
  options: readonly string[];
  prefix: string;
}) {
  const translate = useTranslations("admin.catalog.version");
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
                {options.map((option) => (
                  <SelectItem key={option} value={option}>
                    {translate(`${prefix}Value.${option}`)}
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
    .filter((value) => value.length > 0);
}
