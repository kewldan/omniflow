"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Switch } from "@omniflow/ui/switch";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { FormField } from "@/components/forms/field";
import { type ApiError, fetcher } from "@/lib/api";
import { type Listing, type PlanSummary, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

type Setting = {
  enabled: boolean;
  planId: string;
  periodDays: number;
  trafficGb: number;
  deviceAddonId: string;
  deviceQuantity: number;
};

type AddonSummary = { id: string; code: string; kind: string; archivedAt?: string };

const NONE = "none";
const KEY = "/v1/panel/catalog/simple-subscription";

/**
 * Bedolaga's "simple subscription": one plan and one Buy button in the bot
 * and the web store, its period, traffic, and extra devices chosen here. The
 * server checks the plan and the add-on; the period and the traffic are
 * checked by pricing them when a customer presses Buy.
 */
export function SimpleSubscriptionCard() {
  const translate = useTranslations("admin.catalog.simple");
  const { data, mutate } = useSWR<Setting, ApiError>(KEY, fetcher);
  if (!data) {
    return <Skeleton className="h-48 w-full" />;
  }
  return (
    <SimpleForm
      initial={data}
      key={JSON.stringify(data)}
      onSaved={() => void mutate()}
      title={translate("title")}
    />
  );
}

function SimpleForm({
  initial,
  onSaved,
  title,
}: {
  initial: Setting;
  onSaved: () => void;
  title: string;
}) {
  const translate = useTranslations("admin.catalog.simple");
  const { can } = useSession();
  const { run, pending, error } = useOperatorAction();
  const enabledId = useId();
  const { data: plans } = useSWR<Listing<PlanSummary>, ApiError>(
    "/v1/panel/catalog/plans",
    fetcher,
  );
  const { data: addons } = useSWR<Listing<AddonSummary>, ApiError>(
    "/v1/panel/catalog/addons",
    fetcher,
  );
  const [enabled, setEnabled] = useState(initial.enabled);
  const [planId, setPlanId] = useState(initial.planId || NONE);
  const [periodDays, setPeriodDays] = useState(
    initial.periodDays ? String(initial.periodDays) : "",
  );
  const [trafficGb, setTrafficGb] = useState(initial.trafficGb ? String(initial.trafficGb) : "");
  const [addonId, setAddonId] = useState(initial.deviceAddonId || NONE);
  const [quantity, setQuantity] = useState(String(initial.deviceQuantity));
  const [reason, setReason] = useState("");
  const deviceAddons = (addons?.items ?? []).filter(
    (addon) => addon.kind === "devices" && !addon.archivedAt,
  );
  const writable = can("catalog.write");

  async function save() {
    const body: Setting = {
      deviceAddonId: addonId === NONE ? "" : addonId,
      deviceQuantity: addonId === NONE ? 0 : Number(quantity.trim() || "0"),
      enabled,
      periodDays: Number(periodDays.trim() || "0"),
      planId: planId === NONE ? "" : planId,
      trafficGb: Number(trafficGb.trim() || "0"),
    };
    if (await run(KEY, { body, method: "PUT", reason: reason.trim() })) {
      setReason("");
      onSaved();
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex items-center gap-3">
          <Switch
            checked={enabled}
            disabled={!writable}
            id={enabledId}
            onCheckedChange={setEnabled}
          />
          <Label htmlFor={enabledId}>{translate("enabled")}</Label>
        </div>
        <div className="grid gap-3 sm:grid-cols-3">
          <FormField hint={translate("planHint")} label={translate("plan")}>
            {(props) => (
              <Select disabled={!writable} onValueChange={setPlanId} value={planId}>
                <SelectTrigger {...props}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE}>{translate("none")}</SelectItem>
                  {(plans?.items ?? [])
                    .filter((plan) => !plan.archivedAt)
                    .map((plan) => (
                      <SelectItem key={plan.id} value={plan.id}>
                        {plan.code}
                      </SelectItem>
                    ))}
                </SelectContent>
              </Select>
            )}
          </FormField>
          <FormField hint={translate("periodHint")} label={translate("periodDays")}>
            {(props) => (
              <Input
                {...props}
                disabled={!writable}
                inputMode="numeric"
                onChange={(event) => setPeriodDays(event.target.value)}
                value={periodDays}
              />
            )}
          </FormField>
          <FormField hint={translate("trafficHint")} label={translate("trafficGb")}>
            {(props) => (
              <Input
                {...props}
                disabled={!writable}
                inputMode="numeric"
                onChange={(event) => setTrafficGb(event.target.value)}
                value={trafficGb}
              />
            )}
          </FormField>
          <FormField label={translate("deviceAddon")}>
            {(props) => (
              <Select disabled={!writable} onValueChange={setAddonId} value={addonId}>
                <SelectTrigger {...props}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NONE}>{translate("none")}</SelectItem>
                  {deviceAddons.map((addon) => (
                    <SelectItem key={addon.id} value={addon.id}>
                      {addon.code}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </FormField>
          {addonId !== NONE && (
            <FormField label={translate("deviceQuantity")}>
              {(props) => (
                <Input
                  {...props}
                  disabled={!writable}
                  inputMode="numeric"
                  onChange={(event) => setQuantity(event.target.value)}
                  value={quantity}
                />
              )}
            </FormField>
          )}
        </div>
        {writable && (
          <>
            <FormField label={translate("reason")}>
              {(props) => (
                <Input
                  {...props}
                  onChange={(event) => setReason(event.target.value)}
                  value={reason}
                />
              )}
            </FormField>
            {error && (
              <p className="text-danger-foreground text-sm" role="alert">
                {error.message}
              </p>
            )}
            <Button className="self-start" disabled={pending} onClick={save} size="sm">
              {translate("save")}
            </Button>
          </>
        )}
      </CardContent>
    </Card>
  );
}
