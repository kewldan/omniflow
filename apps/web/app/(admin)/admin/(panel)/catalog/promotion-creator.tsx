"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Checkbox } from "@omniflow/ui/checkbox";
import { DateTimeField } from "@omniflow/ui/date-time-field";
import { Input, Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { type Listing, type PlanSummary, useOperatorAction } from "@/lib/operations";

/** Reward kinds the API accepts, discounts first. */
const KINDS = [
  "percent",
  "fixed",
  "wallet_credit",
  "days",
  "trial",
  "traffic",
  "balance_days",
  "loyalty_tier",
] as const;
type Kind = (typeof KINDS)[number];

const MONEY_KINDS: Kind[] = ["fixed", "wallet_credit"];
const CURRENCY_KINDS: Kind[] = ["fixed", "wallet_credit", "balance_days"];

/**
 * Create a promotion and the codes that redeem it. A discount (percent or
 * fixed) applies at checkout to the plans ticked; every other kind rewards on
 * its own when a customer redeems a code — days, a trial, wallet credit,
 * traffic, days with credit, or a loyalty group for a number of days.
 */
export function PromotionCreator({ onCreated }: { onCreated: () => void }) {
  const translate = useTranslations("admin.catalog.promotionCreator");
  const ids = {
    code: useId(),
    kind: useId(),
    value: useId(),
    currency: useId(),
    credit: useId(),
    tier: useId(),
    perCustomer: useId(),
    limit: useId(),
    starts: useId(),
    ends: useId(),
    codes: useId(),
  };
  const { data: plans } = useSWR<Listing<PlanSummary>, ApiError>(
    "/v1/panel/catalog/plans",
    fetcher,
  );
  const [kind, setKind] = useState<Kind>("percent");
  const [form, setForm] = useState({
    code: "",
    value: "",
    currency: "RUB",
    credit: "",
    tier: "",
    perCustomer: "1",
    limit: "",
    starts: "",
    ends: "",
    codes: "",
  });
  const [planIds, setPlanIds] = useState<string[]>([]);
  const { run, pending, error } = useOperatorAction();
  const set = (patch: Partial<typeof form>) => setForm((current) => ({ ...current, ...patch }));

  function amount(): number {
    const raw = Number(form.value.replace(",", ".")) || 0;
    if (kind === "percent") {
      return Math.round(raw * 100);
    }
    return MONEY_KINDS.includes(kind) ? Math.round(raw * 100) : Math.round(raw);
  }

  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const ok = await run("/v1/panel/catalog/promotions", {
      method: "POST",
      reason: "promotion created",
      body: {
        code: form.code.trim().toLowerCase(),
        kind,
        value: amount(),
        currency: CURRENCY_KINDS.includes(kind) ? form.currency.trim().toUpperCase() : undefined,
        creditMinor:
          kind === "balance_days"
            ? Math.round((Number(form.credit.replace(",", ".")) || 0) * 100)
            : undefined,
        tierCode: kind === "loyalty_tier" ? form.tier.trim().toLowerCase() : undefined,
        perCustomerLimit: Number(form.perCustomer) || 1,
        redemptionLimit: form.limit ? Number(form.limit) : undefined,
        startsAt: form.starts ? new Date(form.starts).toISOString() : undefined,
        endsAt: form.ends ? new Date(form.ends).toISOString() : undefined,
        planIds: kind === "percent" || kind === "fixed" ? planIds : [],
        codes: form.codes
          .split(/[\s,]+/)
          .map((code) => code.trim())
          .filter(Boolean),
        active: true,
      },
    });
    if (ok) {
      toast.success(translate("created"));
      setForm({ ...form, code: "", value: "", credit: "", codes: "" });
      setPlanIds([]);
      onCreated();
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent>
        <form className="flex flex-col gap-3" onSubmit={create}>
          <div className="grid gap-3 sm:grid-cols-3">
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.code}>{translate("code")}</Label>
              <Input
                id={ids.code}
                onChange={(event) => set({ code: event.target.value })}
                placeholder="black_friday"
                value={form.code}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.kind}>{translate("kind")}</Label>
              <Select onValueChange={(value) => setKind(value as Kind)} value={kind}>
                <SelectTrigger id={ids.kind}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {KINDS.map((value) => (
                    <SelectItem key={value} value={value}>
                      {translate(`kinds.${value}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.value}>{translate(`value.${kind}`)}</Label>
              <Input
                id={ids.value}
                inputMode="decimal"
                onChange={(event) => set({ value: event.target.value })}
                value={form.value}
              />
            </div>
            {CURRENCY_KINDS.includes(kind) && (
              <div className="flex flex-col gap-1">
                <Label htmlFor={ids.currency}>{translate("currency")}</Label>
                <Input
                  id={ids.currency}
                  maxLength={3}
                  onChange={(event) => set({ currency: event.target.value })}
                  value={form.currency}
                />
              </div>
            )}
            {kind === "balance_days" && (
              <div className="flex flex-col gap-1">
                <Label htmlFor={ids.credit}>{translate("credit")}</Label>
                <Input
                  id={ids.credit}
                  inputMode="decimal"
                  onChange={(event) => set({ credit: event.target.value })}
                  value={form.credit}
                />
              </div>
            )}
            {kind === "loyalty_tier" && (
              <div className="flex flex-col gap-1">
                <Label htmlFor={ids.tier}>{translate("tier")}</Label>
                <Input
                  id={ids.tier}
                  onChange={(event) => set({ tier: event.target.value })}
                  placeholder="vip"
                  value={form.tier}
                />
              </div>
            )}
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.perCustomer}>{translate("perCustomer")}</Label>
              <Input
                id={ids.perCustomer}
                inputMode="numeric"
                onChange={(event) => set({ perCustomer: event.target.value })}
                value={form.perCustomer}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.limit}>{translate("limit")}</Label>
              <Input
                id={ids.limit}
                inputMode="numeric"
                onChange={(event) => set({ limit: event.target.value })}
                placeholder={translate("unlimited")}
                value={form.limit}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.starts}>{translate("startsAt")}</Label>
              <DateTimeField
                hourLabel={translate("hour")}
                id={ids.starts}
                minuteLabel={translate("minute")}
                onChange={(starts) => set({ starts })}
                placeholder={translate("now")}
                value={form.starts}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={ids.ends}>{translate("endsAt")}</Label>
              <DateTimeField
                fromDate={new Date()}
                hourLabel={translate("hour")}
                id={ids.ends}
                minuteLabel={translate("minute")}
                onChange={(ends) => set({ ends })}
                placeholder={translate("never")}
                value={form.ends}
              />
            </div>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.codes}>{translate("codes")}</Label>
            <Textarea
              id={ids.codes}
              onChange={(event) => set({ codes: event.target.value })}
              placeholder="BLACKFRIDAY, BF-VIP"
              rows={2}
              value={form.codes}
            />
          </div>
          {(kind === "percent" || kind === "fixed") && (
            <div className="flex flex-col gap-1">
              <span className="text-sm">{translate("plans")}</span>
              <div className="flex flex-wrap gap-3">
                {(plans?.items ?? [])
                  .filter((plan) => !plan.archivedAt)
                  .map((plan) => (
                    <PlanCheckbox
                      checked={planIds.includes(plan.id)}
                      key={plan.id}
                      label={plan.code}
                      onChange={(checked) =>
                        setPlanIds((current) =>
                          checked ? [...current, plan.id] : current.filter((id) => id !== plan.id),
                        )
                      }
                    />
                  ))}
              </div>
              <p className="text-muted-foreground text-xs">{translate("plansHint")}</p>
            </div>
          )}
          {error && (
            <p className="text-destructive text-sm" role="alert">
              {error.status === 409 ? translate("conflict") : translate("invalid")}
            </p>
          )}
          <Button
            className="self-start"
            disabled={pending || !form.code.trim() || !form.value.trim() || !form.codes.trim()}
            type="submit"
          >
            {translate("create")}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}

function PlanCheckbox({
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
