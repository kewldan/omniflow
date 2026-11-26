"use client";

import type {
  PanelSharedDevice,
  PanelSharedDevicePage,
  PanelSharedDeviceStatus,
} from "@omniflow/api-client/generated";
import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
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

const STATUSES: PanelSharedDeviceStatus[] = ["open", "legitimate", "confirmed"];

/**
 * Devices that connected through two or more customers' subscriptions.
 *
 * A family on one tablet and a customer with a second account look the same
 * from here, so the verdict is a person's. "Legitimate" stops the device
 * refusing trials and holding referrals; "confirmed" records the finding.
 * Neither suspends anybody — that stays an action on the customer's own page.
 */
export function SharedDevices({ active }: { active: boolean }) {
  const translate = useTranslations("admin.risk.sharedDevices");
  const { can } = useSession();
  const [status, setStatus] = useState<PanelSharedDeviceStatus>("open");
  const [reason, setReason] = useState("");
  const { run, pending, error } = useOperatorAction();

  const key = active ? `/v1/panel/risk/shared-devices?status=${status}` : null;
  const {
    data,
    error: loadError,
    isLoading,
    mutate,
  } = useSWR<PanelSharedDevicePage, ApiError>(key, fetcher);

  async function review(handle: string, verdict: "legitimate" | "confirmed") {
    const ok = await run(`/v1/panel/risk/shared-devices/${handle}/review`, {
      body: { status: verdict },
      method: "POST",
      reason: reason.trim(),
    });
    if (ok) {
      setReason("");
      await mutate();
    }
  }

  const items = data?.items ?? [];
  const writable = can("risk.write") && status === "open";

  return (
    <div className="flex flex-col gap-3">
      <Card className="flex flex-col gap-3 p-3 sm:flex-row sm:items-center">
        <p className="flex-1 text-muted-foreground text-sm">{translate("explainer")}</p>
        <Select
          onValueChange={(value) => setStatus(value as PanelSharedDeviceStatus)}
          value={status}
        >
          <SelectTrigger aria-label={translate("statusFilter")} className="sm:w-48">
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
      </Card>

      {writable && items.length > 0 && (
        <Card className="p-3">
          <Input
            onChange={(event) => setReason(event.target.value)}
            placeholder={translate("reasonPlaceholder")}
            value={reason}
          />
          {error && <p className="mt-2 text-danger-foreground text-xs">{error.message}</p>}
        </Card>
      )}

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
              onReview={writable ? review : undefined}
              pending={pending || reason.trim().length === 0}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function DeviceCard({
  device,
  onReview,
  pending,
}: {
  device: PanelSharedDevice;
  onReview?: (handle: string, verdict: "legitimate" | "confirmed") => void;
  pending: boolean;
}) {
  const translate = useTranslations("admin.risk.sharedDevices");
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
            ? translate("reopened", {
                accounts: device.reviewedAccounts ?? 0,
                reason: device.reviewReason,
              })
            : translate("verdict", { reason: device.reviewReason })}
        </p>
      )}

      {onReview && (
        <div className="flex gap-2">
          <Button
            disabled={pending}
            onClick={() => onReview(device.handle, "legitimate")}
            size="sm"
            variant="outline"
          >
            {translate("legitimate")}
          </Button>
          <Button
            disabled={pending}
            onClick={() => onReview(device.handle, "confirmed")}
            size="sm"
            variant="destructive"
          >
            {translate("confirm")}
          </Button>
        </div>
      )}
    </Card>
  );
}
