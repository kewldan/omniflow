"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Checkbox } from "@omniflow/ui/checkbox";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { useTranslations } from "next-intl";
import { useId } from "react";
import {
  type Control,
  Controller,
  FormProvider,
  useFieldArray,
  useForm,
  useWatch,
} from "react-hook-form";
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
import type { Listing, PlanVersion } from "@/lib/operations";
import { useOperatorAction } from "@/lib/operations";

import { SquadPicker, squadName, useSquads } from "./squad-picker";

// Each list is exactly what the `plan_versions` CHECK constraints accept and
// internal/commerce implements. The squad selection once offered
// `customer_choice` and `fixed`, which nothing implemented and the database
// refused, so a choosable squad set could not be published at all.
const BILLING_PERIODS = [
  "none",
  "day",
  "week",
  "month",
  "quarter",
  "half_year",
  "year",
  "custom",
] as const;
const SQUAD_SELECTIONS = ["automatic", "optional", "required"] as const;
const UPGRADE_POLICIES = ["forbid", "replace", "extend"] as const;
const DOWNGRADE_POLICIES = ["forbid", "immediate", "at_expiry"] as const;
const CANCELLATION_POLICIES = ["forbid", "immediate", "at_expiry"] as const;
const TRIAL_ELIGIBILITY = ["new_customer", "never_trialled", "any"] as const;

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
  squadIds: z.array(z.string()),
  squadSelection: z.string(),
  // The squads a customer may choose, each with what the customer reads in
  // either language. Labels are required because a blank one is an empty
  // checkbox on the customer's screen.
  offeredSquads: z.array(
    z.object({ labelEn: requiredText(80), labelRu: requiredText(80), squadId: z.string() }),
  ),
  // The add-ons sold with this version. Publishing replaces the set, so the
  // editor always sends it; leaving it out used to publish a version that
  // offered nothing.
  addonIds: z.array(z.string()),
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
    addonIds: current?.addonIds ?? [],
    billingPeriod: current?.billingPeriod ?? "month",
    cancellationPolicy: current?.cancellationPolicy ?? "at_expiry",
    deviceLimit: current?.deviceLimit ? String(current.deviceLimit) : "",
    downgradePolicy: current?.downgradePolicy ?? "at_expiry",
    durationDays: String(Math.round((current?.durationSeconds ?? 2_592_000) / 86_400)),
    graceDays: String(Math.round((current?.gracePeriodSeconds ?? 0) / 86_400)),
    maxSquads: current?.maxSelectableSquads ? String(current.maxSelectableSquads) : "",
    minSquads: String(current?.minSelectableSquads ?? 0),
    offeredSquads: current?.offeredSquads ?? [],
    // Shown in major units, as a customer reads them; stored as minor units.
    prices: Object.entries(current?.prices ?? {}).map(([currency, amount]) => ({
      amount: minorToInput(amount, currency),
      currency,
    })),
    reason: "",
    recurringCapable: current?.recurringCapable ?? false,
    squadIds: current?.squadIds ?? [],
    // A version stored with a value from the old editor starts from the
    // nearest one that exists rather than from a value that cannot publish.
    squadSelection: (SQUAD_SELECTIONS as readonly string[]).includes(current?.squadSelection ?? "")
      ? (current?.squadSelection ?? "automatic")
      : "automatic",
    trafficGb: current?.trafficAllowanceBytes
      ? String(current.trafficAllowanceBytes / GIBIBYTE)
      : "",
    trialEligibility: (TRIAL_ELIGIBILITY as readonly string[]).includes(
      current?.trialEligibility ?? "",
    )
      ? (current?.trialEligibility ?? "new_customer")
      : "new_customer",
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
  const { control, formState, handleSubmit, register, resetField, setValue } = form;
  const squadSelection = useWatch({ control, name: "squadSelection" });
  const offered = useFieldArray({ control, name: "offeredSquads" });
  const { data: squadListing } = useSquads();
  const { run, pending, error } = useOperatorAction();
  const errors = formState.errors;

  async function publish(values: Values) {
    const choosing = values.squadSelection !== "automatic";
    const ok = await run(`/v1/panel/catalog/plans/${planId}/versions`, {
      body: {
        addonIds: values.addonIds,
        billingPeriod: values.billingPeriod,
        cancellationPolicy: values.cancellationPolicy,
        deviceLimit: readWholeNumber(values.deviceLimit),
        downgradePolicy: values.downgradePolicy,
        durationSeconds: Number(values.durationDays) * 86_400,
        gracePeriodSeconds: (readWholeNumber(values.graceDays) ?? 0) * 86_400,
        maxSelectableSquads: choosing ? readWholeNumber(values.maxSquads) : null,
        minSelectableSquads: choosing ? (readWholeNumber(values.minSquads) ?? 0) : 0,
        offeredSquads: choosing
          ? values.offeredSquads
              .filter((squad) => squad.squadId)
              .map((squad) => ({
                labelEn: squad.labelEn.trim(),
                labelRu: squad.labelRu.trim(),
                squadId: squad.squadId,
              }))
          : [],
        prices: readPriceList(values.prices),
        recurringCapable: values.recurringCapable,
        squadIds: values.squadIds,
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
                {(props) => (
                  <Controller
                    control={control}
                    name="squadIds"
                    render={({ field }) => (
                      <SquadPicker
                        control={props}
                        multiple
                        onChange={field.onChange}
                        value={field.value}
                      />
                    )}
                  />
                )}
              </FormField>
              <Choice
                control={control}
                label={translate("squadSelection")}
                name="squadSelection"
                options={SQUAD_SELECTIONS}
                prefix="squadSelection"
              />
              {squadSelection !== "automatic" && (
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

            {squadSelection !== "automatic" && (
              <fieldset className="flex flex-col gap-3 rounded-lg border border-border p-3">
                <legend className="px-1 font-medium text-sm">{translate("offeredSquads")}</legend>
                <p className="text-muted-foreground text-xs">{translate("offeredSquadsHint")}</p>
                {offered.fields.map((row, index) => (
                  <div className="grid gap-2 sm:grid-cols-[2fr_1fr_1fr_auto]" key={row.id}>
                    <FormField label={translate("offeredSquad")}>
                      {(props) => (
                        <Controller
                          control={control}
                          name={`offeredSquads.${index}.squadId`}
                          render={({ field }) => (
                            <SquadPicker
                              control={props}
                              multiple={false}
                              onChange={(next) => {
                                const picked = next[0] ?? "";
                                field.onChange(picked);
                                // A label starts as the panel's own name; the
                                // operator rewrites it for customers.
                                const name = squadName(squadListing, picked);
                                if (name) {
                                  setValue(`offeredSquads.${index}.labelEn`, name);
                                  setValue(`offeredSquads.${index}.labelRu`, name);
                                }
                              }}
                              value={field.value ? [field.value] : []}
                            />
                          )}
                        />
                      )}
                    </FormField>
                    <FormField
                      error={errors.offeredSquads?.[index]?.labelRu?.message}
                      label={translate("labelRu")}
                    >
                      {(props) => (
                        <Input {...props} {...register(`offeredSquads.${index}.labelRu`)} />
                      )}
                    </FormField>
                    <FormField
                      error={errors.offeredSquads?.[index]?.labelEn?.message}
                      label={translate("labelEn")}
                    >
                      {(props) => (
                        <Input {...props} {...register(`offeredSquads.${index}.labelEn`)} />
                      )}
                    </FormField>
                    <Button
                      className="self-end"
                      onClick={() => offered.remove(index)}
                      size="sm"
                      type="button"
                      variant="ghost"
                    >
                      {translate("removeSquad")}
                    </Button>
                  </div>
                ))}
                <Button
                  className="self-start"
                  onClick={() => offered.append({ labelEn: "", labelRu: "", squadId: "" })}
                  size="sm"
                  type="button"
                  variant="outline"
                >
                  {translate("addSquad")}
                </Button>
              </fieldset>
            )}

            <AddonOffer control={control} />

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

type AddonSummary = { id: string; code: string; kind: string; archivedAt?: string };

/**
 * The add-ons sold with this version, as a list of checkboxes.
 *
 * Publishing replaces the offer set rather than merging it, so the editor
 * carries the previous version's set forward and lets the operator change it.
 */
function AddonOffer({ control }: { control: Control<Values> }) {
  const translate = useTranslations("admin.catalog.version");
  const { data } = useSWR<Listing<AddonSummary>, ApiError>("/v1/panel/catalog/addons", fetcher);
  const addons = (data?.items ?? []).filter((addon) => !addon.archivedAt);
  if (addons.length === 0) {
    return null;
  }
  return (
    <Controller
      control={control}
      name="addonIds"
      render={({ field }) => (
        <fieldset className="flex flex-col gap-2">
          <legend className="font-medium text-sm">{translate("addons")}</legend>
          <div className="flex flex-wrap gap-4">
            {addons.map((addon) => (
              <AddonCheckbox
                checked={field.value.includes(addon.id)}
                key={addon.id}
                label={`${addon.code} (${addon.kind})`}
                onChange={(checked) =>
                  field.onChange(
                    checked
                      ? [...field.value, addon.id]
                      : field.value.filter((id) => id !== addon.id),
                  )
                }
              />
            ))}
          </div>
        </fieldset>
      )}
    />
  );
}

function AddonCheckbox({
  checked,
  label,
  onChange,
}: {
  checked: boolean;
  label: string;
  onChange: (checked: boolean) => void;
}) {
  const id = useId();
  return (
    <div className="flex items-center gap-2">
      <Checkbox checked={checked} id={id} onCheckedChange={(state) => onChange(state === true)} />
      <Label htmlFor={id}>{label}</Label>
    </div>
  );
}
