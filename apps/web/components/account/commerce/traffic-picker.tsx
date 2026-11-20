"use client";

import { Button } from "@omniflow/ui/button";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";

import { useBytes, useMoney } from "@/lib/format";

import type { CheckoutView } from "./types";

const OWN = "own";
const CUSTOM = "custom";

/**
 * The traffic volume for this checkout: the plan's own allowance, one of its
 * traffic packages, or a number of gigabytes priced by the gigabyte.
 *
 * Only a plan whose traffic is selectable sends packages or a range. The
 * server prices whatever is chosen and refuses a volume the plan does not
 * sell, so the bounds shown are guidance rather than the check.
 */
export function TrafficPicker({
  checkout,
  disabled,
  onChange,
}: {
  checkout: CheckoutView;
  disabled: boolean;
  onChange: (body: Record<string, unknown>) => void;
}) {
  const translate = useTranslations("account.trafficPicker");
  const money = useMoney();
  const bytes = useBytes();
  const selectId = useId();
  const volumeId = useId();
  const traffic = checkout.traffic;
  const packages = traffic?.packages ?? [];
  const chosen = checkout.trafficGb ?? 0;
  const listed = packages.some((pack) => pack.gb === chosen);
  const [mode, setMode] = useState(chosen === 0 ? OWN : listed ? String(chosen) : CUSTOM);
  const [volume, setVolume] = useState(chosen > 0 && !listed ? String(chosen) : "");

  const custom = traffic?.custom;
  if (!traffic || (packages.length === 0 && !custom)) {
    return null;
  }
  const own = traffic.ownBytes === null ? translate("unlimited") : bytes(traffic.ownBytes);

  return (
    <fieldset className="space-y-2 rounded-lg border border-border bg-card p-4">
      <legend className="px-1 font-medium text-sm">{translate("title")}</legend>
      <Label htmlFor={selectId}>{translate("label")}</Label>
      <Select
        disabled={disabled}
        onValueChange={(value) => {
          setMode(value);
          if (value === OWN) {
            onChange({ trafficGb: 0 });
          } else if (value !== CUSTOM) {
            onChange({ trafficGb: Number(value) });
          }
        }}
        value={mode}
      >
        <SelectTrigger id={selectId}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={OWN}>{translate("own", { traffic: own })}</SelectItem>
          {packages.map((pack) => (
            <SelectItem key={pack.gb} value={String(pack.gb)}>
              {translate("package", {
                gb: pack.gb,
                price: money(pack.price.amountMinor, pack.price.currency),
              })}
            </SelectItem>
          ))}
          {custom && <SelectItem value={CUSTOM}>{translate("custom")}</SelectItem>}
        </SelectContent>
      </Select>
      {mode === CUSTOM && custom && (
        <div className="flex items-end gap-2">
          <div className="flex flex-1 flex-col gap-1.5">
            <Label htmlFor={volumeId}>
              {translate("volume", { max: custom.maxGb, min: custom.minGb })}
            </Label>
            <Input
              id={volumeId}
              inputMode="numeric"
              onChange={(event) => setVolume(event.target.value)}
              value={volume}
            />
            <span className="text-muted-foreground text-xs">
              {translate("perGb", {
                price: money(custom.pricePerGb.amountMinor, custom.pricePerGb.currency),
              })}
            </span>
          </div>
          <Button
            disabled={disabled || !/^\d+$/.test(volume.trim())}
            onClick={() => onChange({ trafficGb: Number(volume.trim()) })}
            size="sm"
            type="button"
            variant="outline"
          >
            {translate("apply")}
          </Button>
        </div>
      )}
    </fieldset>
  );
}
