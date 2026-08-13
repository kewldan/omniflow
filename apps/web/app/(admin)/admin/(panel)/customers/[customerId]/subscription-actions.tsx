"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { DateTimeField } from "@omniflow/ui/date-time-field";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { type Listing, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

type PanelDevice = {
  ref: string;
  platform?: string;
  model?: string;
  osVersion?: string;
  lastSeenAt: string;
};

/**
 * Lifecycle actions for one subscription.
 *
 * Every action goes through the fulfillment pipeline rather than writing to
 * Remnawave directly, so an operator change carries the same idempotency key,
 * retry policy, drift detection, and history as one a purchase produced. That
 * is why the buttons report "queued" rather than "done": the worker applies it.
 */
export function SubscriptionActions({
  customerId,
  subscriptionId,
  hasEntitlement,
}: {
  customerId: string;
  subscriptionId: string;
  hasEntitlement: boolean;
}) {
  const translate = useTranslations("admin.customers");
  const { can } = useSession();
  const endsAtId = useId();
  const trafficId = useId();
  const deviceLimitId = useId();
  const reasonId = useId();

  const [reason, setReason] = useState("");
  const [endsAt, setEndsAt] = useState("");
  const [traffic, setTraffic] = useState("");
  const [deviceLimit, setDeviceLimit] = useState("");
  const [queued, setQueued] = useState("");
  const { run, pending, error } = useOperatorAction();

  if (!can("subscriptions.write")) {
    return null;
  }

  async function enqueue(operation: string, body: Record<string, unknown> = {}) {
    setQueued("");
    const ok = await run(
      `/v1/panel/customers/${customerId}/subscriptions/${subscriptionId}/operations`,
      { body: { operation, ...body }, method: "POST", reason: reason.trim() },
    );
    if (ok) {
      setQueued(operation);
      setReason("");
    }
  }

  const ready = reason.trim().length > 0 && hasEntitlement && !pending;

  return (
    <Card className="mt-3 flex flex-col gap-3 p-3">
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={reasonId}>{translate("actions.reason")}</Label>
        <Input
          id={reasonId}
          onChange={(event) => setReason(event.target.value)}
          placeholder={translate("actions.reasonPlaceholder")}
          value={reason}
        />
      </div>

      {!hasEntitlement && (
        // A slot that has never been provisioned has nothing to change.
        <p className="text-muted-foreground text-xs">{translate("actions.notProvisioned")}</p>
      )}

      <div className="flex flex-wrap gap-2">
        <Button disabled={!ready} onClick={() => enqueue("enable")} size="sm" variant="outline">
          {translate("actions.enable")}
        </Button>
        <Button disabled={!ready} onClick={() => enqueue("disable")} size="sm" variant="outline">
          {translate("actions.disable")}
        </Button>
        <Button
          disabled={!ready}
          onClick={() => enqueue("reset_traffic")}
          size="sm"
          variant="outline"
        >
          {translate("actions.resetTraffic")}
        </Button>
      </div>

      <div className="grid gap-3 sm:grid-cols-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={endsAtId}>{translate("actions.endsAt")}</Label>
          <DateTimeField
            hourLabel={translate("actions.hour")}
            id={endsAtId}
            minuteLabel={translate("actions.minute")}
            onChange={setEndsAt}
            placeholder={translate("actions.pickMoment")}
            value={endsAt}
          />
          <Button
            disabled={!ready || endsAt === ""}
            onClick={() => enqueue("extend", { endsAt: new Date(endsAt).toISOString() })}
            size="sm"
            variant="outline"
          >
            {translate("actions.extend")}
          </Button>
        </div>

        <div className="flex flex-col gap-1.5">
          <Label htmlFor={trafficId}>{translate("actions.trafficBytes")}</Label>
          <Input
            id={trafficId}
            inputMode="numeric"
            onChange={(event) => setTraffic(event.target.value)}
            value={traffic}
          />
        </div>

        <div className="flex flex-col gap-1.5">
          <Label htmlFor={deviceLimitId}>{translate("actions.deviceLimit")}</Label>
          <Input
            id={deviceLimitId}
            inputMode="numeric"
            onChange={(event) => setDeviceLimit(event.target.value)}
            value={deviceLimit}
          />
          <Button
            disabled={!ready || (traffic === "" && deviceLimit === "")}
            onClick={() =>
              enqueue("set_limits", {
                deviceLimit: deviceLimit === "" ? null : Number(deviceLimit),
                trafficAllowanceBytes: traffic === "" ? null : Number(traffic),
              })
            }
            size="sm"
            variant="outline"
          >
            {translate("actions.setLimits")}
          </Button>
        </div>
      </div>

      {error && <p className="text-danger-foreground text-sm">{error.message}</p>}
      {queued && (
        // The worker applies it, so the honest report is "queued", not "done".
        <p className="text-muted-foreground text-sm">
          {translate("actions.queued", { operation: queued })}
        </p>
      )}
    </Card>
  );
}

/**
 * Connected devices for one subscription.
 *
 * The hardware identifier never reaches the browser. The list shows what an
 * operator needs to recognise a device — platform, model, when it was last seen
 * — and addresses it by an opaque reference the server resolves back.
 */
export function SubscriptionDevices({
  customerId,
  subscriptionId,
}: {
  customerId: string;
  subscriptionId: string;
}) {
  const translate = useTranslations("admin.customers");
  const locale = useLocale();
  const { can } = useSession();
  const [reason, setReason] = useState("");
  const { run, pending } = useOperatorAction();

  const base = `/v1/panel/customers/${customerId}/subscriptions/${subscriptionId}/devices`;
  const { data, error, isLoading, mutate } = useSWR<Listing<PanelDevice>, ApiError>(base, fetcher);

  if (isLoading) {
    return <Skeleton className="mt-3 h-20 w-full" />;
  }
  if (error) {
    // Remnawave is authoritative for devices, so its being unreachable is a
    // stated limitation of this panel rather than a broken page.
    return <p className="mt-3 text-muted-foreground text-xs">{translate("devices.unavailable")}</p>;
  }

  const items = data?.items ?? [];
  if (items.length === 0) {
    return <p className="mt-3 text-muted-foreground text-xs">{translate("devices.empty")}</p>;
  }

  return (
    <div className="mt-3 flex flex-col gap-2">
      <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
        {translate("devices.title")}
      </p>
      {can("subscriptions.write") && (
        <Input
          onChange={(event) => setReason(event.target.value)}
          placeholder={translate("devices.reasonPlaceholder")}
          value={reason}
        />
      )}
      {items.map((device) => (
        <div className="flex items-center justify-between gap-3 text-sm" key={device.ref}>
          <span className="flex items-center gap-2">
            <Badge variant="neutral">{device.platform || translate("devices.unknown")}</Badge>
            <span className="text-muted-foreground">
              {device.model || "—"}
              {device.osVersion ? ` · ${device.osVersion}` : ""}
            </span>
          </span>
          <span className="flex items-center gap-2">
            <span className="font-mono text-[11px] text-muted-foreground">
              {new Date(device.lastSeenAt).toLocaleDateString(locale)}
            </span>
            {can("subscriptions.write") && (
              <Button
                disabled={pending || reason.trim().length === 0}
                onClick={async () => {
                  const ok = await run(`${base}/${device.ref}`, {
                    method: "DELETE",
                    reason: reason.trim(),
                  });
                  if (ok) {
                    setReason("");
                    await mutate();
                  }
                }}
                size="sm"
                variant="ghost"
              >
                {translate("devices.remove")}
              </Button>
            )}
          </span>
        </div>
      ))}
    </div>
  );
}
