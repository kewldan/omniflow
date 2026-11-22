"use client";

import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Input } from "@omniflow/ui/input";
import { toast } from "@omniflow/ui/toast";
import { Laptop, Smartphone, TabletSmartphone } from "lucide-react";
import Link from "next/link";
import { useFormatter, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { ReauthNotice, useReauthentication } from "@/components/account/reauth";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import type { AccountSubscription } from "@/components/account/subscription-card";
import { useAccount } from "@/lib/account-session";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";

type Device = {
  handle: string;
  name?: string;
  platform?: string;
  lastSeen: string;
  /** The customer's own name for the device. */
  alias?: string;
};
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
 * A subscription's connected devices, with renaming and removal.
 *
 * The devices screen shows it on its own, with the count as the headline. The
 * dashboard embeds it under the subscription when there is only one: most
 * customers have exactly one, and sending them to a second screen to free a
 * slot was a detour. Embedded, the count is left to the subscription above it
 * and the empty state leaves connecting to the Connect button.
 */
export function DeviceList({
  embedded = false,
  onChanged,
  subscription,
}: {
  embedded?: boolean;
  /** Called after a removal, so a screen showing the device count can refresh it. */
  onChanged?: () => void;
  subscription: AccountSubscription;
}) {
  const translate = useTranslations("account");
  const format = useFormatter();
  const key = `/v1/account/subscriptions/${subscription.id}/devices`;
  const { data, error, isLoading, mutate } = useSWR<{ items: Device[] }, ApiError>(key, fetcher);
  const [pendingHandle, setPendingHandle] = useState<string | null>(null);
  const [removeAllOpen, setRemoveAllOpen] = useState(false);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [aliasDraft, setAliasDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const { session } = useAccount();
  const { redirectIfRequired } = useReauthentication();
  const needsReauth = session?.session.reauthenticationRequired ?? false;

  async function removeDevice(handle: string) {
    setBusy(true);
    try {
      await apiFetch(`${key}/${encodeURIComponent(handle)}`, { method: "DELETE" });
      await mutate();
      onChanged?.();
      toast.success(translate("devices.removed"));
    } catch (removeError) {
      toast.error((removeError as ApiError).message);
    } finally {
      setBusy(false);
      setPendingHandle(null);
    }
  }

  async function rename(handle: string) {
    setBusy(true);
    try {
      await apiFetch(`${key}/${encodeURIComponent(handle)}`, {
        body: JSON.stringify({ alias: aliasDraft }),
        method: "PATCH",
      });
      await mutate();
      setRenaming(null);
      toast.success(translate("devices.renamed"));
    } catch (renameError) {
      toast.error((renameError as ApiError).message);
    } finally {
      setBusy(false);
    }
  }

  async function removeAll() {
    setBusy(true);
    try {
      await apiFetch(`${key}?confirm=true`, { method: "DELETE" });
      await mutate();
      onChanged?.();
      toast.success(translate("devices.removedAll"));
    } catch (removeError) {
      // A stale session is a distinct outcome with a distinct remedy: the
      // customer is sent to sign in again and returned here.
      if (!redirectIfRequired(removeError)) {
        toast.error((removeError as ApiError).message);
      }
    } finally {
      setBusy(false);
      setRemoveAllOpen(false);
    }
  }

  if (isLoading) {
    return <ListSkeleton rows={embedded ? 2 : undefined} />;
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
      {/* Embedded, the subscription right above already says how many slots
          are taken, so the list starts at its heading. */}
      {embedded ? (
        <SectionLabel>{translate("devices.title")}</SectionLabel>
      ) : (
        <>
          <div className="rounded-lg border border-border bg-card p-4">
            <div className="flex items-baseline justify-between">
              <span className="font-bold text-[30px] leading-none tracking-[-0.04em]" data-numeric>
                {devices.length}
              </span>
              <span className="text-[12.5px] text-subtle-foreground">{usage}</span>
            </div>
          </div>
          <SectionLabel>{translate("devices.title")}</SectionLabel>
        </>
      )}

      {devices.length === 0 ? (
        <AccountNotice
          action={
            embedded ? undefined : (
              <Button asChild>
                <Link href={`/account/subscriptions/${subscription.id}/connect`}>
                  {translate("devices.connect")}
                </Link>
              </Button>
            )
          }
          description={translate("devices.emptyDescription")}
          title={translate("devices.empty")}
        />
      ) : (
        // One grouped card with rows, rather than a separate box per device:
        // a stack of boxes read as a grid of squares.
        <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
          {devices.map((device) => {
            const Icon = deviceIcon(device.platform);
            return (
              <li className="flex animate-rise items-center gap-3 px-4 py-3" key={device.handle}>
                <span className="flex size-10 shrink-0 items-center justify-center rounded-md bg-muted">
                  <Icon aria-hidden className="size-[19px] text-muted-foreground" />
                </span>
                <div className="min-w-0 flex-1">
                  {renaming === device.handle ? (
                    <form
                      className="flex gap-2"
                      onSubmit={(event) => {
                        event.preventDefault();
                        void rename(device.handle);
                      }}
                    >
                      <Input
                        aria-label={translate("devices.alias")}
                        autoFocus
                        maxLength={40}
                        onChange={(event) => setAliasDraft(event.target.value)}
                        value={aliasDraft}
                      />
                      <Button disabled={busy} size="sm" type="submit">
                        {translate("devices.saveAlias")}
                      </Button>
                    </form>
                  ) : (
                    <button
                      className="block max-w-full truncate text-left font-semibold text-[15px] hover:underline"
                      onClick={() => {
                        setAliasDraft(device.alias ?? "");
                        setRenaming(device.handle);
                      }}
                      title={translate("devices.rename")}
                      type="button"
                    >
                      {device.alias || device.name || translate("devices.unknown")}
                    </button>
                  )}
                  {/* The model, when a name of the customer's own replaced it,
                      and the date, on one line. A date, not a timestamp: the
                      customer needs to recognise their own device, not to be
                      handed a movement log. */}
                  <p className="mt-0.5 truncate text-[13px] text-muted-foreground">
                    {[
                      device.alias && device.name ? device.name : null,
                      format.dateTime(new Date(device.lastSeen), {
                        day: "numeric",
                        month: "short",
                      }),
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </p>
                </div>
                <Button
                  className="text-destructive"
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

      {devices.length > 1 && needsReauth && <ReauthNotice />}
      {devices.length > 1 && (
        // A quiet control: it is the rare, destructive one, and as a
        // full-width outlined button it outweighed everything above it.
        <Button
          className="mx-auto flex text-destructive"
          disabled={busy}
          onClick={() => setRemoveAllOpen(true)}
          size="sm"
          variant="ghost"
        >
          {translate("devices.removeAll")}
        </Button>
      )}

      {!embedded && (
        <p className="rounded-lg border border-border bg-card p-4 text-[12.5px] text-muted-foreground leading-relaxed">
          {translate("devices.hint")}
        </p>
      )}

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
