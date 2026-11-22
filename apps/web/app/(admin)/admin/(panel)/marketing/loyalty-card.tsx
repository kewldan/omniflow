"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Checkbox } from "@omniflow/ui/checkbox";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { toast } from "@omniflow/ui/toast";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";
import { useEnumLabel } from "@/components/admin/enum-label";
import { type ApiError, fetcher } from "@/lib/api";
import { type Listing, type PlanSummary, useOperatorAction } from "@/lib/operations";

/** One rung of a loyalty programme — a promo group in Bedolaga's terms. */
export type LoyaltyTier = {
  id?: string;
  code: string;
  nameEn: string;
  nameRu: string;
  threshold: number;
  discountBps: number;
  periodDiscounts: Record<string, number>;
  trafficDiscountBps: number;
  deviceDiscountBps: number;
  squadDiscountBps: number;
  priority: number;
  manualOnly: boolean;
  allowedPlanIds: string[];
  allowedSquadIds: string[];
  allowedProviders: string[];
};

export type LoyaltyProgram = {
  id: string;
  version: number;
  enabled: boolean;
  metric: string;
  currency: string;
  windowDays: number;
  graceDays: number;
  tiers: LoyaltyTier[];
};

/** Billing periods a period discount may name; mirrors the plan catalogue. */
const PERIODS = ["month", "quarter", "half_year", "year"] as const;

type TierDraft = {
  key: string;
  code: string;
  nameEn: string;
  nameRu: string;
  threshold: string;
  discount: string;
  periods: Record<string, string>;
  traffic: string;
  devices: string;
  squads: string;
  priority: string;
  manualOnly: boolean;
  plans: string[];
  providers: string;
  squadIds: string;
};

const percent = (bps: number) => (bps ? String(bps / 100) : "");
const bps = (value: string) => Math.round((Number(value.replace(",", ".")) || 0) * 100);
const list = (value: string) =>
  value
    .split(/[\s,]+/)
    .map((item) => item.trim())
    .filter(Boolean);

function draftFrom(tier: LoyaltyTier): TierDraft {
  return {
    key: crypto.randomUUID(),
    code: tier.code,
    nameEn: tier.nameEn,
    nameRu: tier.nameRu,
    threshold: String(tier.threshold),
    discount: percent(tier.discountBps),
    periods: Object.fromEntries(
      PERIODS.map((period) => [period, percent(tier.periodDiscounts?.[period] ?? 0)]),
    ),
    traffic: percent(tier.trafficDiscountBps),
    devices: percent(tier.deviceDiscountBps),
    squads: percent(tier.squadDiscountBps),
    priority: String(tier.priority ?? 0),
    manualOnly: tier.manualOnly,
    plans: tier.allowedPlanIds ?? [],
    providers: (tier.allowedProviders ?? []).join(", "),
    squadIds: (tier.allowedSquadIds ?? []).join(", "),
  };
}

function emptyDraft(): TierDraft {
  return draftFrom({
    code: "",
    nameEn: "",
    nameRu: "",
    threshold: 0,
    discountBps: 0,
    periodDiscounts: {},
    trafficDiscountBps: 0,
    deviceDiscountBps: 0,
    squadDiscountBps: 0,
    priority: 0,
    manualOnly: false,
    allowedPlanIds: [],
    allowedSquadIds: [],
    allowedProviders: [],
  });
}

function tierFrom(draft: TierDraft): LoyaltyTier {
  const periodDiscounts: Record<string, number> = {};
  for (const period of PERIODS) {
    const value = draft.periods[period] ?? "";
    if (value.trim() !== "") {
      periodDiscounts[period] = bps(value);
    }
  }
  return {
    code: draft.code.trim().toLowerCase(),
    nameEn: draft.nameEn.trim(),
    nameRu: draft.nameRu.trim(),
    threshold: Number(draft.threshold) || 0,
    discountBps: bps(draft.discount),
    periodDiscounts,
    trafficDiscountBps: bps(draft.traffic),
    deviceDiscountBps: bps(draft.devices),
    squadDiscountBps: bps(draft.squads),
    priority: Number(draft.priority) || 0,
    manualOnly: draft.manualOnly,
    allowedPlanIds: draft.plans,
    allowedSquadIds: list(draft.squadIds),
    allowedProviders: list(draft.providers),
  };
}

/**
 * Loyalty tiers as Bedolaga's promo groups: the ladder customers climb by
 * spend, orders, or tenure, plus manual groups an operator grants. Each tier
 * discounts plans (optionally by billing period) and add-ons by kind, may keep
 * its members to some plans, squads, and payment methods, and has a priority
 * that decides which group prices an order. A programme is never edited:
 * publishing writes the next version, and the worker re-places every customer.
 */
export function LoyaltyCard({ editable }: { editable: boolean }) {
  const translate = useTranslations("admin.marketing.loyalty");
  const label = useEnumLabel();
  const locale = useLocale();
  const { data, isLoading, mutate } = useSWR<Listing<LoyaltyProgram>, ApiError>(
    "/v1/panel/loyalty/programs?pageSize=5",
    fetcher,
  );
  const current = (data?.items ?? []).find((program) => program.enabled);
  const [editing, setEditing] = useState(false);
  const { run, pending } = useOperatorAction();

  async function recalculate() {
    if (
      await run("/v1/panel/loyalty/recalculate", {
        method: "POST",
        reason: "loyalty recalculation",
      })
    ) {
      toast.success(translate("recalculationRequested"));
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : !current ? (
          <p className="text-muted-foreground text-sm">{translate("empty")}</p>
        ) : (
          <>
            <p className="text-sm">
              {translate("summary", {
                version: current.version,
                metric: translate(`metrics.${current.metric}`),
                window: current.windowDays,
                grace: current.graceDays,
              })}
            </p>
            <div className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{translate("columns.tier")}</TableHead>
                    <TableHead className="text-right">{translate("columns.threshold")}</TableHead>
                    <TableHead className="text-right">{translate("columns.discount")}</TableHead>
                    <TableHead>{translate("columns.addons")}</TableHead>
                    <TableHead className="text-right">{translate("columns.priority")}</TableHead>
                    <TableHead>{translate("columns.restrictions")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {current.tiers.map((tier) => (
                    <TableRow key={tier.id ?? tier.code}>
                      <TableCell>
                        <div className="font-medium">
                          {locale === "ru" ? tier.nameRu : tier.nameEn}
                        </div>
                        <div className="font-mono text-muted-foreground text-xs">
                          {tier.code}
                          {tier.manualOnly && (
                            <Badge className="ml-2" variant="outline">
                              {translate("manual")}
                            </Badge>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className="text-right">
                        {tier.manualOnly ? "—" : tier.threshold}
                      </TableCell>
                      <TableCell className="text-right">
                        {percent(tier.discountBps) || "0"}%
                        {Object.entries(tier.periodDiscounts ?? {}).map(([period, value]) => (
                          <div className="text-muted-foreground text-xs" key={period}>
                            {translate(`periods.${period}`)}: {percent(value) || "0"}%
                          </div>
                        ))}
                      </TableCell>
                      <TableCell className="text-xs">
                        {translate("addonsLine", {
                          traffic: percent(tier.trafficDiscountBps) || "0",
                          devices: percent(tier.deviceDiscountBps) || "0",
                          squads: percent(tier.squadDiscountBps) || "0",
                        })}
                      </TableCell>
                      <TableCell className="text-right">{tier.priority}</TableCell>
                      <TableCell className="text-xs">
                        {translate("restrictionsLine", {
                          plans: tier.allowedPlanIds?.length ?? 0,
                          squads: tier.allowedSquadIds?.length ?? 0,
                          providers:
                            (tier.allowedProviders ?? [])
                              .map((provider) => label("provider", provider))
                              .join(", ") || "—",
                        })}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          </>
        )}
        {editable && (
          <div className="flex flex-wrap gap-2">
            <Button onClick={() => setEditing((open) => !open)} variant="outline">
              {editing ? translate("closeEditor") : translate("openEditor")}
            </Button>
            {current && (
              <Button disabled={pending} onClick={() => void recalculate()} variant="outline">
                {translate("recalculate")}
              </Button>
            )}
          </div>
        )}
        {editable && editing && (
          <LoyaltyEditor
            current={current}
            onPublished={() => {
              setEditing(false);
              void mutate();
            }}
          />
        )}
      </CardContent>
    </Card>
  );
}

function LoyaltyEditor({
  current,
  onPublished,
}: {
  current?: LoyaltyProgram;
  onPublished: () => void;
}) {
  const translate = useTranslations("admin.marketing.loyalty");
  const ids = {
    metric: useId(),
    currency: useId(),
    window: useId(),
    grace: useId(),
    enable: useId(),
  };
  const { data: plans } = useSWR<Listing<PlanSummary>, ApiError>(
    "/v1/panel/catalog/plans",
    fetcher,
  );
  const [metric, setMetric] = useState(current?.metric ?? "spend");
  const [currency, setCurrency] = useState(current?.currency ?? "RUB");
  const [windowDays, setWindowDays] = useState(String(current?.windowDays ?? 365));
  const [graceDays, setGraceDays] = useState(String(current?.graceDays ?? 30));
  const [enable, setEnable] = useState(true);
  const [tiers, setTiers] = useState<TierDraft[]>(
    current?.tiers.length ? current.tiers.map(draftFrom) : [emptyDraft()],
  );
  const { run, pending, error } = useOperatorAction();

  function setTier(index: number, patch: Partial<TierDraft>) {
    setTiers((all) => all.map((tier, at) => (at === index ? { ...tier, ...patch } : tier)));
  }

  async function publish(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const ok = await run("/v1/panel/loyalty/programs", {
      method: "POST",
      reason: "loyalty programme published",
      body: {
        metric,
        currency: currency.trim().toUpperCase(),
        windowDays: Number(windowDays) || 0,
        graceDays: Number(graceDays) || 0,
        enable,
        tiers: tiers.map(tierFrom),
      },
    });
    if (ok) {
      toast.success(translate("published"));
      onPublished();
    }
  }

  return (
    <form className="flex flex-col gap-4 rounded-md border p-4" onSubmit={publish}>
      <div className="flex items-center gap-2">
        <Switch checked={enable} id={ids.enable} onCheckedChange={setEnable} />
        <Label htmlFor={ids.enable}>{translate("enable")}</Label>
      </div>
      <div className="grid items-end gap-3 sm:grid-cols-4">
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.metric}>{translate("metric")}</Label>
          <Select onValueChange={setMetric} value={metric}>
            <SelectTrigger id={ids.metric}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {["spend", "orders", "tenure"].map((value) => (
                <SelectItem key={value} value={value}>
                  {translate(`metrics.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.currency}>{translate("currency")}</Label>
          <Input
            id={ids.currency}
            onChange={(event) => setCurrency(event.target.value)}
            value={currency}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.window}>{translate("windowDays")}</Label>
          <Input
            id={ids.window}
            inputMode="numeric"
            onChange={(event) => setWindowDays(event.target.value)}
            value={windowDays}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={ids.grace}>{translate("graceDays")}</Label>
          <Input
            id={ids.grace}
            inputMode="numeric"
            onChange={(event) => setGraceDays(event.target.value)}
            value={graceDays}
          />
        </div>
      </div>
      <p className="text-muted-foreground text-xs">{translate(`thresholdHint.${metric}`)}</p>

      {tiers.map((tier, index) => (
        <fieldset className="flex flex-col gap-3 rounded-md border p-3" key={tier.key}>
          <legend className="px-1 font-medium text-sm">
            {translate("tierLegend", { number: index + 1 })}
          </legend>
          <div className="grid gap-3 sm:grid-cols-4">
            <TierInput
              label={translate("code")}
              onChange={(code) => setTier(index, { code })}
              value={tier.code}
            />
            <TierInput
              label={translate("nameRu")}
              onChange={(nameRu) => setTier(index, { nameRu })}
              value={tier.nameRu}
            />
            <TierInput
              label={translate("nameEn")}
              onChange={(nameEn) => setTier(index, { nameEn })}
              value={tier.nameEn}
            />
            <TierInput
              disabled={tier.manualOnly}
              label={translate("threshold")}
              numeric
              onChange={(threshold) => setTier(index, { threshold })}
              value={tier.threshold}
            />
            <TierInput
              label={translate("discount")}
              numeric
              onChange={(discount) => setTier(index, { discount })}
              value={tier.discount}
            />
            {PERIODS.map((period) => (
              <TierInput
                key={period}
                label={translate("periodDiscount", { period: translate(`periods.${period}`) })}
                numeric
                onChange={(value) =>
                  setTier(index, { periods: { ...tier.periods, [period]: value } })
                }
                value={tier.periods[period] ?? ""}
              />
            ))}
            <TierInput
              label={translate("trafficDiscount")}
              numeric
              onChange={(traffic) => setTier(index, { traffic })}
              value={tier.traffic}
            />
            <TierInput
              label={translate("deviceDiscount")}
              numeric
              onChange={(devices) => setTier(index, { devices })}
              value={tier.devices}
            />
            <TierInput
              label={translate("squadDiscount")}
              numeric
              onChange={(squads) => setTier(index, { squads })}
              value={tier.squads}
            />
            <TierInput
              label={translate("priority")}
              numeric
              onChange={(priority) => setTier(index, { priority })}
              value={tier.priority}
            />
            <TierInput
              label={translate("providers")}
              onChange={(providers) => setTier(index, { providers })}
              placeholder="yookassa, telegram_stars"
              value={tier.providers}
            />
            <TierInput
              label={translate("squadIds")}
              onChange={(squadIds) => setTier(index, { squadIds })}
              value={tier.squadIds}
            />
          </div>
          <ManualSwitch
            checked={tier.manualOnly}
            label={translate("manualOnly")}
            onChange={(manualOnly) => setTier(index, { manualOnly })}
          />
          <div className="flex flex-col gap-1">
            <span className="text-sm">{translate("plans")}</span>
            <div className="flex flex-wrap gap-3">
              {(plans?.items ?? [])
                .filter((plan) => !plan.archivedAt)
                .map((plan) => (
                  <PlanToggle
                    checked={tier.plans.includes(plan.id)}
                    key={plan.id}
                    label={plan.code}
                    onChange={(checked) =>
                      setTier(index, {
                        plans: checked
                          ? [...tier.plans, plan.id]
                          : tier.plans.filter((id) => id !== plan.id),
                      })
                    }
                  />
                ))}
            </div>
            <p className="text-muted-foreground text-xs">{translate("plansHint")}</p>
          </div>
          <Button
            className="self-start"
            disabled={tiers.length === 1}
            onClick={() => setTiers((all) => all.filter((_, at) => at !== index))}
            type="button"
            variant="outline"
          >
            {translate("removeTier")}
          </Button>
        </fieldset>
      ))}
      <Button
        className="self-start"
        onClick={() => setTiers((all) => [...all, emptyDraft()])}
        type="button"
        variant="outline"
      >
        {translate("addTier")}
      </Button>
      {error && (
        <p className="text-destructive text-sm" role="alert">
          {translate("invalid")}
        </p>
      )}
      <Button className="self-start" disabled={pending} type="submit">
        {translate("publish")}
      </Button>
    </form>
  );
}

function TierInput({
  disabled,
  label,
  numeric,
  onChange,
  placeholder,
  value,
}: {
  disabled?: boolean;
  label: string;
  numeric?: boolean;
  onChange: (value: string) => void;
  placeholder?: string;
  value: string;
}) {
  const id = useId();
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{label}</Label>
      <Input
        disabled={disabled}
        id={id}
        inputMode={numeric ? "decimal" : undefined}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
        value={value}
      />
    </div>
  );
}

function ManualSwitch({
  checked,
  label,
  onChange,
}: {
  checked: boolean;
  label: string;
  onChange: (value: boolean) => void;
}) {
  const id = useId();
  return (
    <div className="flex items-center gap-2">
      <Switch checked={checked} id={id} onCheckedChange={onChange} />
      <Label htmlFor={id}>{label}</Label>
    </div>
  );
}

function PlanToggle({
  checked,
  label,
  onChange,
}: {
  checked: boolean;
  label: string;
  onChange: (value: boolean) => void;
}) {
  const id = useId();
  return (
    <div className="flex items-center gap-2">
      <Checkbox checked={checked} id={id} onCheckedChange={(value) => onChange(value === true)} />
      <Label className="font-mono text-xs" htmlFor={id}>
        {label}
      </Label>
    </div>
  );
}
