"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { DateTimeField } from "@omniflow/ui/date-time-field";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import { type Listing, useOperatorAction } from "@/lib/operations";
import type { LoyaltyProgram } from "../../marketing/loyalty-card";

type CustomerLoyalty = {
  standing: {
    tierCode: string;
    nameEn: string;
    nameRu: string;
    discountBps: number;
    evaluatedMetric: number;
    graceUntil?: string;
  };
  groups: { tierCode: string; source: string; note?: string; expiresAt?: string }[] | null;
  history: { fromTier?: string; toTier: string; reason: string; occurredAt: string }[] | null;
};

/**
 * Where a customer stands in the loyalty programme: the tier their metric
 * earned, the groups an operator or a promo code granted, and the history of
 * both. An operator with marketing.write grants or removes a group by hand and
 * re-evaluates the earned tier.
 */
export function LoyaltyPanel({
  active,
  customerId,
  editable,
}: {
  active: boolean;
  customerId: string;
  editable: boolean;
}) {
  const translate = useTranslations("admin.customers.loyalty");
  const locale = useLocale();
  const path = `/v1/panel/customers/${customerId}/loyalty`;
  const { data, isLoading, mutate } = useSWR<CustomerLoyalty, ApiError>(
    active ? path : null,
    fetcher,
  );
  const { data: programs } = useSWR<Listing<LoyaltyProgram>, ApiError>(
    active ? "/v1/panel/loyalty/programs?pageSize=5" : null,
    fetcher,
  );
  const tiers = (programs?.items ?? []).find((program) => program.enabled)?.tiers ?? [];
  // A tier by its name, the way the operator wrote it; a tier that is no
  // longer in the programme keeps its code.
  const tierName = (code?: string) => {
    const tier = tiers.find((entry) => entry.code === code);
    if (!tier) {
      return code || "∅";
    }
    return (locale === "ru" ? tier.nameRu : tier.nameEn) || tier.code;
  };
  const ids = { tier: useId(), note: useId(), until: useId() };
  const [tierCode, setTierCode] = useState("");
  const [note, setNote] = useState("");
  const [until, setUntil] = useState("");
  const { run, pending } = useOperatorAction();

  async function act(url: string, method: string, body?: unknown) {
    if (await run(url, { method, body, reason: "loyalty group" })) {
      void mutate();
      return true;
    }
    return false;
  }

  if (isLoading) {
    return <Skeleton className="h-24 w-full" />;
  }
  const standing = data?.standing;
  return (
    <Card className="flex flex-col gap-3 p-4">
      <div className="font-medium">{translate("title")}</div>
      <p className="text-sm">
        {standing?.tierCode
          ? translate("earned", {
              tier: locale === "ru" ? standing.nameRu : standing.nameEn,
              metric: standing.evaluatedMetric,
            })
          : translate("noTier")}
      </p>
      <div className="flex flex-wrap gap-2">
        {(data?.groups ?? []).map((group) => (
          <Badge className="gap-2" key={group.tierCode} variant="outline">
            <span>{tierName(group.tierCode)}</span>
            <span className="text-muted-foreground">{translate(`sources.${group.source}`)}</span>
            {group.expiresAt && <Timestamp value={group.expiresAt} />}
            {editable && (
              <button
                aria-label={translate("remove", { tier: tierName(group.tierCode) })}
                className="text-destructive"
                disabled={pending}
                onClick={() => void act(`${path}/groups/${group.tierCode}`, "DELETE")}
                type="button"
              >
                ×
              </button>
            )}
          </Badge>
        ))}
        {(data?.groups ?? []).length === 0 && (
          <span className="text-muted-foreground text-sm">{translate("noGroups")}</span>
        )}
      </div>
      {editable && (
        <form
          className="grid gap-3 sm:grid-cols-[12rem_1fr_14rem_auto]"
          onSubmit={async (event) => {
            event.preventDefault();
            const ok = await act(`${path}/groups`, "POST", {
              tierCode,
              note: note.trim(),
              expiresAt: until ? new Date(until).toISOString() : undefined,
            });
            if (ok) {
              setNote("");
              setUntil("");
            }
          }}
        >
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.tier}>{translate("group")}</Label>
            <Select onValueChange={setTierCode} value={tierCode}>
              <SelectTrigger id={ids.tier}>
                <SelectValue placeholder={translate("pickGroup")} />
              </SelectTrigger>
              <SelectContent>
                {tiers.map((tier) => (
                  <SelectItem key={tier.code} value={tier.code}>
                    {locale === "ru" ? tier.nameRu : tier.nameEn}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.note}>{translate("note")}</Label>
            <Input
              id={ids.note}
              maxLength={400}
              onChange={(event) => setNote(event.target.value)}
              value={note}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.until}>{translate("until")}</Label>
            <DateTimeField
              fromDate={new Date()}
              hourLabel={translate("hour")}
              id={ids.until}
              minuteLabel={translate("minute")}
              onChange={setUntil}
              placeholder={translate("forever")}
              value={until}
            />
          </div>
          <Button className="self-end" disabled={pending || !tierCode} type="submit">
            {translate("assign")}
          </Button>
        </form>
      )}
      {editable && (
        <Button
          className="self-start"
          disabled={pending}
          onClick={() => void act(`${path}/evaluate`, "POST")}
          variant="outline"
        >
          {translate("evaluate")}
        </Button>
      )}
      {(data?.history ?? []).length > 0 && (
        <ul className="flex flex-col gap-1 text-xs">
          {(data?.history ?? []).map((change) => (
            <li key={`${change.occurredAt}-${change.toTier}`}>
              <Timestamp value={change.occurredAt} /> — {tierName(change.fromTier)} →{" "}
              {tierName(change.toTier)} ({translate(`reasons.${change.reason}`)})
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
