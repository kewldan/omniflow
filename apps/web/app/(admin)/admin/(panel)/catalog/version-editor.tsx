"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Switch } from "@omniflow/ui/switch";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";

import type { PlanVersion } from "@/lib/operations";
import { useOperatorAction } from "@/lib/operations";

const BILLING_PERIODS = ["day", "week", "month", "quarter", "year", "custom"] as const;
const SQUAD_SELECTIONS = ["automatic", "customer_choice", "fixed"] as const;
const UPGRADE_POLICIES = ["forbid", "replace", "extend"] as const;
const DOWNGRADE_POLICIES = ["forbid", "immediate", "at_expiry"] as const;
const CANCELLATION_POLICIES = ["immediate", "at_expiry"] as const;
const TRIAL_ELIGIBILITY = ["new_customer", "never", "any_customer"] as const;

/**
 * Publishes the next version of a plan.
 *
 * There is no edit. A plan version is immutable once an order references it, so
 * an editor that changed one would silently re-price history; this publishes
 * the next version and leaves the old one costing what it cost. The form is
 * pre-filled from the current version because a price change is usually the
 * only thing that differs.
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
  const translate = useTranslations("admin.catalog.version");
  const periodId = useId();
  const durationId = useId();
  const trafficId = useId();
  const devicesId = useId();
  const graceId = useId();
  const squadsId = useId();
  const minSquadsId = useId();
  const maxSquadsId = useId();
  const pricesId = useId();
  const recurringId = useId();
  const reasonId = useId();

  const [billingPeriod, setBillingPeriod] = useState(current?.billingPeriod ?? "month");
  const [durationDays, setDurationDays] = useState(
    String(Math.round((current?.durationSeconds ?? 2_592_000) / 86_400)),
  );
  const [trafficGb, setTrafficGb] = useState(
    current?.trafficAllowanceBytes ? String(current.trafficAllowanceBytes / 1_073_741_824) : "",
  );
  const [deviceLimit, setDeviceLimit] = useState(
    current?.deviceLimit ? String(current.deviceLimit) : "",
  );
  const [squadIds, setSquadIds] = useState((current?.squadIds ?? []).join(", "));
  const [squadSelection, setSquadSelection] = useState(current?.squadSelection ?? "automatic");
  const [minSquads, setMinSquads] = useState(String(current?.minSelectableSquads ?? 0));
  const [maxSquads, setMaxSquads] = useState(
    current?.maxSelectableSquads ? String(current.maxSelectableSquads) : "",
  );
  const [upgradePolicy, setUpgradePolicy] = useState(current?.upgradePolicy ?? "extend");
  const [downgradePolicy, setDowngradePolicy] = useState(current?.downgradePolicy ?? "at_expiry");
  const [cancellationPolicy, setCancellationPolicy] = useState(
    current?.cancellationPolicy ?? "at_expiry",
  );
  const [graceDays, setGraceDays] = useState(
    String(Math.round((current?.gracePeriodSeconds ?? 0) / 86_400)),
  );
  const [trialEligibility, setTrialEligibility] = useState(
    current?.trialEligibility ?? "new_customer",
  );
  const [recurringCapable, setRecurringCapable] = useState(current?.recurringCapable ?? false);
  const [prices, setPrices] = useState(
    Object.entries(current?.prices ?? {})
      .map(([currency, amount]) => `${currency}=${amount}`)
      .join(", "),
  );
  const [reason, setReason] = useState("");

  const { run, pending, error } = useOperatorAction();
  const parsedPrices = parsePrices(prices);
  const ready =
    Number(durationDays) > 0 &&
    Object.keys(parsedPrices).length > 0 &&
    reason.trim().length > 0 &&
    !pending;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid gap-3 sm:grid-cols-3">
          <Choice
            id={periodId}
            label={translate("billingPeriod")}
            onChange={setBillingPeriod}
            options={BILLING_PERIODS}
            prefix="billingPeriod"
            value={billingPeriod}
          />
          <Field
            hint={translate("durationHint")}
            id={durationId}
            label={translate("duration")}
            onChange={setDurationDays}
            value={durationDays}
          />
          <Field
            hint={translate("graceHint")}
            id={graceId}
            label={translate("grace")}
            onChange={setGraceDays}
            value={graceDays}
          />
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          {/* Empty means unlimited, which is a different thing from zero — zero
              would be a plan that permits nothing. */}
          <Field
            hint={translate("trafficHint")}
            id={trafficId}
            label={translate("traffic")}
            onChange={setTrafficGb}
            value={trafficGb}
          />
          <Field
            hint={translate("devicesHint")}
            id={devicesId}
            label={translate("devices")}
            onChange={setDeviceLimit}
            value={deviceLimit}
          />
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          <Field
            hint={translate("squadsHint")}
            id={squadsId}
            label={translate("squads")}
            onChange={setSquadIds}
            value={squadIds}
          />
          <Choice
            id={`${squadsId}-selection`}
            label={translate("squadSelection")}
            onChange={setSquadSelection}
            options={SQUAD_SELECTIONS}
            prefix="squadSelection"
            value={squadSelection}
          />
          {squadSelection === "customer_choice" && (
            <>
              <Field
                id={minSquadsId}
                label={translate("minSquads")}
                onChange={setMinSquads}
                value={minSquads}
              />
              <Field
                hint={translate("maxSquadsHint")}
                id={maxSquadsId}
                label={translate("maxSquads")}
                onChange={setMaxSquads}
                value={maxSquads}
              />
            </>
          )}
        </div>

        <div className="grid gap-3 sm:grid-cols-3">
          <Choice
            id={`${periodId}-upgrade`}
            label={translate("upgradePolicy")}
            onChange={setUpgradePolicy}
            options={UPGRADE_POLICIES}
            prefix="upgradePolicy"
            value={upgradePolicy}
          />
          <Choice
            id={`${periodId}-downgrade`}
            label={translate("downgradePolicy")}
            onChange={setDowngradePolicy}
            options={DOWNGRADE_POLICIES}
            prefix="downgradePolicy"
            value={downgradePolicy}
          />
          <Choice
            id={`${periodId}-cancellation`}
            label={translate("cancellationPolicy")}
            onChange={setCancellationPolicy}
            options={CANCELLATION_POLICIES}
            prefix="cancellationPolicy"
            value={cancellationPolicy}
          />
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          <Choice
            id={`${periodId}-trial`}
            label={translate("trialEligibility")}
            onChange={setTrialEligibility}
            options={TRIAL_ELIGIBILITY}
            prefix="trialEligibility"
            value={trialEligibility}
          />
          <div className="flex items-center gap-3 pt-6">
            <Switch
              checked={recurringCapable}
              id={recurringId}
              onCheckedChange={setRecurringCapable}
            />
            <Label htmlFor={recurringId}>{translate("recurringCapable")}</Label>
          </div>
        </div>

        <Field
          hint={translate("pricesHint")}
          id={pricesId}
          label={translate("prices")}
          onChange={setPrices}
          value={prices}
        />
        <Field
          hint={translate("reasonHint")}
          id={reasonId}
          label={translate("reason")}
          onChange={setReason}
          value={reason}
        />

        <p className="text-muted-foreground text-xs">{translate("immutability")}</p>
        {error && <p className="text-danger-foreground text-sm">{error.message}</p>}
        <Button
          className="self-start"
          disabled={!ready}
          onClick={async () => {
            const ok = await run(`/v1/panel/catalog/plans/${planId}/versions`, {
              body: {
                billingPeriod,
                cancellationPolicy,
                deviceLimit: deviceLimit === "" ? null : Number(deviceLimit),
                downgradePolicy,
                durationSeconds: Number(durationDays) * 86_400,
                gracePeriodSeconds: Number(graceDays || 0) * 86_400,
                maxSelectableSquads: maxSquads === "" ? null : Number(maxSquads),
                minSelectableSquads: Number(minSquads || 0),
                prices: parsedPrices,
                recurringCapable,
                squadIds: splitList(squadIds),
                squadSelection,
                trafficAllowanceBytes:
                  trafficGb === "" ? null : Math.round(Number(trafficGb) * 1_073_741_824),
                trialEligibility,
                upgradePolicy,
              },
              method: "POST",
              reason: reason.trim(),
            });
            if (ok) {
              setReason("");
              onPublished();
            }
          }}
          size="sm"
        >
          {translate("publish")}
        </Button>
      </CardContent>
    </Card>
  );
}

function Field({
  hint,
  id,
  label,
  onChange,
  value,
}: {
  hint?: string;
  id: string;
  label: string;
  onChange: (value: string) => void;
  value: string;
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input id={id} onChange={(event) => onChange(event.target.value)} value={value} />
      {hint && <span className="text-muted-foreground text-xs">{hint}</span>}
    </div>
  );
}

function Choice({
  id,
  label,
  onChange,
  options,
  prefix,
  value,
}: {
  id: string;
  label: string;
  onChange: (value: string) => void;
  options: readonly string[];
  prefix: string;
  value: string;
}) {
  const translate = useTranslations("admin.catalog.version");
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <select
        className="h-9 rounded-md border border-border bg-transparent px-2 text-sm"
        id={id}
        onChange={(event) => onChange(event.target.value)}
        value={value}
      >
        {options.map((option) => (
          <option key={option} value={option}>
            {translate(`${prefix}Value.${option}`)}
          </option>
        ))}
      </select>
    </div>
  );
}

/**
 * Parses `RUB=49900, USD=599` into the map the API expects.
 *
 * Amounts are integers in the currency's minor unit throughout Omniflow, and
 * this deliberately does not accept a decimal: "4.99" is ambiguous about which
 * currency's minor unit it means, and the whole money model exists to avoid
 * that ambiguity.
 */
function parsePrices(input: string): Record<string, number> {
  const prices: Record<string, number> = {};
  for (const entry of splitList(input)) {
    const [currency, amount] = entry.split("=");
    if (!currency || !amount) {
      continue;
    }
    const minor = Number(amount.trim());
    if (!Number.isInteger(minor) || minor < 0) {
      continue;
    }
    prices[currency.trim().toUpperCase()] = minor;
  }
  return prices;
}

function splitList(input: string): string[] {
  return input
    .split(",")
    .map((value) => value.trim())
    .filter((value) => value.length > 0);
}
