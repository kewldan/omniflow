"use client";

import type {
  PanelSharedDevice,
  PanelSharedDevicePage,
  PanelSharedDeviceStatus,
} from "@omniflow/api-client/generated";
import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { CustomerChip } from "@/components/admin/customer-chip";
import { StateNotice } from "@/components/admin/state-notice";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";
import { BanDeviceDialog } from "./ban-device-dialog";

const STATUSES: PanelSharedDeviceStatus[] = ["open", "legitimate", "confirmed"];
/** "Any number of accounts" or "more than two". */
const SIZES = ["2", "3"] as const;

/**
 * Devices that connected through two or more customers' subscriptions.
 *
 * A family on one tablet and a customer with a second account look the same
 * from here, so the verdict is a person's. "Legitimate" stops the device
 * refusing trials and holding referrals; "Ban" opens a dialog that suspends
 * the chosen accounts, for how long and why, and can disable their
 * subscriptions.
 */
export function SharedDevices({ active }: { active: boolean }) {
  const translate = useTranslations("admin.risk.sharedDevices");
  const { can } = useSession();
  const [status, setStatus] = useState<PanelSharedDeviceStatus>("open");
  const [size, setSize] = useState<string>("2");
  const [banning, setBanning] = useState<PanelSharedDevice | null>(null);
  const { run, pending, error } = useOperatorAction();

  const key = active
    ? `/v1/panel/risk/shared-devices?status=${status}${size === "2" ? "" : `&minAccounts=${size}`}`
    : null;
  const {
    data,
    error: loadError,
    isLoading,
    mutate,
  } = useSWR<PanelSharedDevicePage, ApiError>(key, fetcher);

  async function legitimate(handle: string) {
    if (
      await run(`/v1/panel/risk/shared-devices/${handle}/review`, {
        body: { status: "legitimate" },
        method: "POST",
      })
    ) {
      await mutate();
    }
  }

  const items = data?.items ?? [];
  const open = can("risk.write") && status === "open";
  const canBan = open && can("customers.write");

  return (
    <div className="flex flex-col gap-3">
      <Card className="flex flex-col gap-3 p-3 lg:flex-row lg:items-center">
        <p className="flex-1 text-muted-foreground text-sm">{translate("explainer")}</p>
        <div className="flex flex-col gap-2 sm:flex-row">
          <Select onValueChange={setSize} value={size}>
            <SelectTrigger aria-label={translate("sizeFilter")} className="sm:w-56">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {SIZES.map((value) => (
                <SelectItem key={value} value={value}>
                  {translate(`size.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select
            onValueChange={(value) => setStatus(value as PanelSharedDeviceStatus)}
            value={status}
          >
            <SelectTrigger aria-label={translate("statusFilter")} className="sm:w-56">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {STATUSES.map((value) => (
                <SelectItem key={value} value={value}>
                  {translate(`status.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </Card>

      {error && <p className="text-danger-foreground text-xs">{error.message}</p>}

      {isLoading ? (
        <Skeleton className="h-32 w-full" />
      ) : loadError ? (
        <StateNotice description={loadError.message} title={translate("failed")} variant="danger" />
      ) : items.length === 0 ? (
        <StateNotice
          description={translate("emptyDescription")}
          title={translate("empty")}
          variant="empty"
        />
      ) : (
        <div className="grid gap-3 lg:grid-cols-2">
          {items.map((device) => (
            <DeviceCard
              device={device}
              key={device.handle}
              onBan={canBan ? () => setBanning(device) : undefined}
              onLegitimate={open ? () => legitimate(device.handle) : undefined}
              pending={pending}
            />
          ))}
        </div>
      )}

      <BanDeviceDialog
        device={banning}
        onBanned={() => {
          setBanning(null);
          void mutate();
        }}
        onOpenChange={(next) => {
          if (!next) {
            setBanning(null);
          }
        }}
      />
    </div>
  );
}

function DeviceCard({
  device,
  onBan,
  onLegitimate,
  pending,
}: {
  device: PanelSharedDevice;
  onBan?: () => void;
  onLegitimate?: () => void;
  pending: boolean;
}) {
  const translate = useTranslations("admin.risk.sharedDevices");
  const reasons = useTranslations("admin.moderation.ban.templates");
  const banReason = device.reviewReason?.startsWith("shared_device:")
    ? device.reviewReason.slice("shared_device:".length)
    : "";
  const verdict =
    device.reviewReason === "legitimate"
      ? translate("status.legitimate")
      : banReason
        ? translate("bannedFor", {
            reason: reasons.has(banReason) ? reasons(banReason) : banReason,
          })
        : (device.reviewReason ?? "");
  const reopened =
    device.status === "open" && device.reviewedAt !== undefined && device.reviewReason;

  return (
    <Card className="flex flex-col gap-3 p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="flex flex-col gap-0.5">
          <span className="font-medium">{translate("accounts", { count: device.accounts })}</span>
          <span className="text-[11px] text-muted-foreground">
            {device.platform ? `${device.platform} · ` : ""}
            <span className="font-mono">{device.handle.slice(0, 10)}</span>
          </span>
        </div>
        <div className="flex flex-wrap justify-end gap-1">
          {device.trialAccounts > 1 && (
            <Badge variant="danger">{translate("trials", { count: device.trialAccounts })}</Badge>
          )}
          {device.referralPairs > 0 && (
            <Badge variant="warning">
              {translate("referrals", { count: device.referralPairs })}
            </Badge>
          )}
          {device.status !== "open" && (
            <Badge variant={device.status === "confirmed" ? "danger" : "success"}>
              {translate(`status.${device.status}`)}
            </Badge>
          )}
        </div>
      </div>

      <ul className="flex flex-col gap-2">
        {device.customers.map((account) => (
          <li
            className="flex flex-wrap items-center justify-between gap-2 text-sm"
            key={account.customerId}
          >
            <CustomerChip id={account.customerId} />
            <span className="flex flex-wrap items-center gap-2 text-muted-foreground text-xs">
              {account.trialClaimed && <Badge variant="neutral">{translate("trial")}</Badge>}
              {account.invitedBy && <Badge variant="neutral">{translate("invited")}</Badge>}
              <span>
                <Timestamp value={account.firstSeenAt} /> — <Timestamp value={account.lastSeenAt} />
              </span>
            </span>
          </li>
        ))}
      </ul>

      {device.reviewReason && (
        <p className="text-muted-foreground text-xs">
          {reopened
            ? translate("reopened", { accounts: device.reviewedAccounts ?? 0, reason: verdict })
            : translate("verdict", { reason: verdict })}
        </p>
      )}

      {(onLegitimate || onBan) && (
        <div className="flex gap-2">
          {onLegitimate && (
            <Button disabled={pending} onClick={onLegitimate} size="sm" variant="outline">
              {translate("legitimate")}
            </Button>
          )}
          {onBan && (
            <Button disabled={pending} onClick={onBan} size="sm" variant="destructive">
              {translate("ban.open")}
            </Button>
          )}
        </div>
      )}
    </Card>
  );
}
