"use client";

import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { cn } from "@omniflow/ui/lib/utils";
import { toast } from "@omniflow/ui/toast";
import { Laptop, Smartphone, TabletSmartphone } from "lucide-react";
import Link from "next/link";
import { useFormatter, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import type { AccountSubscription } from "@/components/account/subscription-card";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";

type Device = { handle: string; name?: string; platform?: string; lastSeen: string };
type Overview = { subscriptions: AccountSubscription[]; showSwitcher: boolean };

/** Picks an icon from the platform the client reported, defaulting to a phone. */
function deviceIcon(platform: string | undefined) {
  const value = (platform ?? "").toLowerCase();
  if (value.includes("mac") || value.includes("win") || value.includes("linux")) {
    return Laptop;
  }
  if (value.includes("pad") || value.includes("tablet")) {
    return TabletSmartphone;
  }
  return Smartphone;
}

/**
 * The devices screen.
 *
 * Devices belong to a subscription rather than to the account, so this picks one
 * — the only one, in a single-subscription installation — and lets the customer
 * change which when there are several.
 */
export default function DevicesPage() {
  const translate = useTranslations("account");
  const { data: overview, isLoading: loadingOverview } = useSWR<Overview, ApiError>(
    "/v1/account/overview",
    fetcher,
  );
  const [subscriptionId, setSubscriptionId] = useState<string | null>(null);

  const subscriptions = overview?.subscriptions ?? [];
  const active = subscriptions.find((item) => item.id === subscriptionId) ?? subscriptions[0];

  if (loadingOverview) {
    return <ListSkeleton />;
  }
  if (!active) {
    return (
      <AccountNotice
        description={translate("dashboard.emptyDescription")}
        title={translate("dashboard.empty")}
      />
    );
  }

  return (
    <div className="space-y-4">
      {subscriptions.length > 1 && (
        <fieldset className="flex flex-wrap gap-2">
          <legend className="sr-only">{translate("dashboard.switcher")}</legend>
          {subscriptions.map((subscription) => (
            <Button
              aria-pressed={subscription.id === active.id}
              key={subscription.id}
              onClick={() => setSubscriptionId(subscription.id)}
              size="sm"
              variant={subscription.id === active.id ? "secondary" : "outline"}
            >
              {subscription.label}
            </Button>
          ))}
        </fieldset>
      )}
      <DeviceList subscription={active} />
    </div>
  );
}

function DeviceList({ subscription }: { subscription: AccountSubscription }) {
  const translate = useTranslations("account");
  const format = useFormatter();
  const key = `/v1/account/subscriptions/${subscription.id}/devices`;
  const { data, error, isLoading, mutate } = useSWR<{ items: Device[] }, ApiError>(key, fetcher);
  const [pendingHandle, setPendingHandle] = useState<string | null>(null);
  const [removeAllOpen, setRemoveAllOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  async function removeDevice(handle: string) {
    setBusy(true);
    try {
      await apiFetch(`${key}/${encodeURIComponent(handle)}`, { method: "DELETE" });
      await mutate();
      toast.success(translate("devices.removed"));
    } catch (removeError) {
      toast.error((removeError as ApiError).message);
    } finally {
      setBusy(false);
      setPendingHandle(null);
    }
  }

  async function removeAll() {
    setBusy(true);
    try {
      await apiFetch(`${key}?confirm=true`, { method: "DELETE" });
      await mutate();
      toast.success(translate("devices.removedAll"));
    } catch (removeError) {
      const problem = removeError as ApiError;
      // A stale session is a distinct outcome with a distinct remedy, so it is
      // reported as "sign in again" rather than as a generic failure.
      toast.error(
        problem.code === "reauthentication_required"
          ? translate("states.reauthenticate")
          : problem.message,
      );
    } finally {
      setBusy(false);
      setRemoveAllOpen(false);
    }
  }

  if (isLoading) {
    return <ListSkeleton />;
  }
  if (error) {
    // A subscription that is not provisioned yet has no device list to read, and
    // the API says so with a 409. That is an ordinary stage of a new purchase,
    // not a failure, and reporting it as "something went wrong" is how the very
    // first screen a new customer opens tells them the product is broken.
    const pending = error.status === 409;
    const offline = error.status === 503;
    return (
      <AccountNotice
        description={
          pending
            ? translate("connect.provisioningDescription")
            : offline
              ? translate("states.upstreamDescription")
              : translate("states.errorDescription")
        }
        title={
          pending
            ? translate("connect.provisioning")
            : offline
              ? translate("states.upstream")
              : translate("states.error")
        }
        variant={offline || pending ? "offline" : "danger"}
      />
    );
  }

  const devices = data?.items ?? [];
  const usage = subscription.devices.unlimited
    ? translate("subscription.devicesUnlimited", { used: devices.length })
    : translate("subscription.devicesUsed", {
        limit: subscription.devices.limit ?? 0,
        used: devices.length,
      });

  return (
    <div className="space-y-4">
      <div className="rounded-lg border border-border bg-card p-4">
        <div className="flex items-baseline justify-between">
          <span className="font-bold text-[30px] leading-none tracking-[-0.04em]" data-numeric>
            {devices.length}
          </span>
          <span className="font-mono text-[11px] text-subtle-foreground">{usage}</span>
        </div>
      </div>

      <SectionLabel>{translate("devices.title")}</SectionLabel>

      {devices.length === 0 ? (
        <AccountNotice
          action={
            <Button asChild>
              <Link href={`/account/subscriptions/${subscription.id}/connect`}>
                {translate("devices.connect")}
              </Link>
            </Button>
          }
          description={translate("devices.emptyDescription")}
          title={translate("devices.empty")}
        />
      ) : (
        <ul className="space-y-3">
          {devices.map((device) => {
            const Icon = deviceIcon(device.platform);
            return (
              <li
                className="flex animate-rise items-center gap-3 rounded-md border border-border bg-card p-4"
                key={device.handle}
              >
                <span className="flex size-10 shrink-0 items-center justify-center rounded-md bg-muted">
                  <Icon aria-hidden className="size-[19px] text-muted-foreground" />
                </span>
                <div className="min-w-0 flex-1">
                  <p className="truncate font-semibold text-[15px]">
                    {device.name || translate("devices.unknown")}
                  </p>
                  {/* A date, not a timestamp: the customer needs to recognise
                      their own device, not to be handed a movement log. */}
                  <p className="mt-1 font-mono text-[11px] text-subtle-foreground">
                    {format.dateTime(new Date(device.lastSeen), {
                      day: "numeric",
                      month: "short",
                      year: "numeric",
                    })}
                  </p>
                </div>
                <Button
                  className={cn("text-destructive")}
                  disabled={busy}
                  onClick={() => setPendingHandle(device.handle)}
                  size="sm"
                  variant="ghost"
                >
                  {translate("devices.remove")}
                </Button>
              </li>
            );
          })}
        </ul>
      )}

      {devices.length > 1 && (
        <Button
          className="w-full text-destructive"
          disabled={busy}
          onClick={() => setRemoveAllOpen(true)}
          size="lg"
          variant="outline"
        >
          {translate("devices.removeAll")}
        </Button>
      )}

      <p className="rounded-lg border border-border bg-card p-4 text-[12.5px] text-muted-foreground leading-relaxed">
        {translate("devices.hint")}
      </p>

      <ConfirmDialog
        cancelLabel={translate("actions.cancel")}
        confirmLabel={translate("devices.remove")}
        description={translate("devices.confirmRemoveDescription")}
        destructive
        onConfirm={() => pendingHandle && removeDevice(pendingHandle)}
        onOpenChange={(open) => !open && setPendingHandle(null)}
        open={pendingHandle !== null}
        pending={busy}
        title={translate("devices.confirmRemove")}
      />
      <ConfirmDialog
        cancelLabel={translate("actions.cancel")}
        confirmLabel={translate("devices.removeAll")}
        description={translate("devices.confirmRemoveAllDescription")}
        destructive
        onConfirm={removeAll}
        onOpenChange={setRemoveAllOpen}
        open={removeAllOpen}
        pending={busy}
        title={translate("devices.confirmRemoveAll")}
      />
    </div>
  );
}
