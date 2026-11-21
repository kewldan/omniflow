"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Input } from "@omniflow/ui/input";
import { Skeleton } from "@omniflow/ui/skeleton";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";
import { useEnumLabel } from "@/components/admin/enum-label";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import { formatBytes, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

type SyncField = {
  field: "expiry" | "traffic" | "device_limit" | "squads" | "status";
  omniflow: unknown;
  remnawave: unknown;
  matches: boolean;
};

type SyncPreview = {
  entitlementId: string;
  entitlementStatus: string;
  remnawaveUserId?: number;
  remoteExists: boolean;
  inSync: boolean;
  fields: SyncField[];
  checkedAt: string;
};

/**
 * "Sync with Remnawave" for one subscription.
 *
 * It first compares — read-only — what Omniflow holds for the subscription with
 * the Remnawave user, field by field. Applying pushes Omniflow's side through
 * the fulfillment pipeline after a confirmation, so the change is queued, retried,
 * and recorded like every other one; the panel never writes to Remnawave itself.
 */
export function SubscriptionSync({
  customerId,
  subscriptionId,
}: {
  customerId: string;
  subscriptionId: string;
}) {
  const translate = useTranslations("admin.customers.sync");
  const label = useEnumLabel();
  const { can } = useSession();
  const [open, setOpen] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [reason, setReason] = useState("");
  const [queued, setQueued] = useState(false);
  const { run, pending, error } = useOperatorAction();
  const path = `/v1/panel/customers/${customerId}/subscriptions/${subscriptionId}/sync`;
  const {
    data,
    error: loadError,
    isLoading,
    mutate,
  } = useSWR<SyncPreview, ApiError>(open ? path : null, fetcher);

  if (!can("subscriptions.read")) {
    return null;
  }
  if (!open) {
    return (
      <Button className="mt-3 self-start" onClick={() => setOpen(true)} size="sm" variant="outline">
        {translate("compare")}
      </Button>
    );
  }

  function display(field: SyncField, value: unknown, side: "omniflow" | "remnawave") {
    if (side === "remnawave" && data && !data.remoteExists) {
      return "—";
    }
    switch (field.field) {
      case "expiry":
        return typeof value === "string" ? <Timestamp mode="datetime" value={value} /> : "—";
      case "traffic":
        // Omniflow spells unlimited as no value, Remnawave as zero.
        return value === null || value === 0 ? translate("unlimited") : formatBytes(Number(value));
      case "device_limit":
        if (value === null || value === undefined) {
          // A null remote limit falls back to the panel's own default.
          return side === "remnawave" ? translate("panelDefault") : translate("unlimited");
        }
        return value === 0 ? translate("unlimited") : String(value);
      case "squads": {
        const squads = Array.isArray(value) ? (value as string[]) : [];
        return squads.length === 0
          ? translate("noSquads")
          : squads.map((squad) => squad.slice(0, 8)).join(", ");
      }
      case "status":
        return typeof value === "string" ? label("entitlementStatus", value.toLowerCase()) : "—";
      default:
        return String(value ?? "—");
    }
  }

  async function apply() {
    const ok = await run(path, { method: "POST", reason: reason.trim() });
    setConfirming(false);
    if (ok) {
      setQueued(true);
      setReason("");
      await mutate();
    }
  }

  return (
    <div className="mt-3 flex flex-col gap-2 rounded-md border border-border p-3">
      <div className="flex items-center justify-between gap-2">
        <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
          {translate("title")}
        </p>
        <Button disabled={isLoading} onClick={() => void mutate()} size="sm" variant="ghost">
          {translate("refresh")}
        </Button>
      </div>
      {isLoading && <Skeleton className="h-24 w-full" />}
      {loadError && (
        <p className="text-muted-foreground text-xs">
          {loadError.status === 409 ? translate("notProvisioned") : translate("unavailable")}
        </p>
      )}
      {data && (
        <>
          <p className="text-sm">
            {!data.remoteExists ? (
              <Badge variant="warning">{translate("missingRemote")}</Badge>
            ) : data.inSync ? (
              <Badge variant="success">{translate("inSync")}</Badge>
            ) : (
              <Badge variant="warning">{translate("outOfSync")}</Badge>
            )}{" "}
            <span className="text-muted-foreground text-xs">
              {translate("checkedAt")} <Timestamp mode="time" value={data.checkedAt} />
            </span>
          </p>
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left text-muted-foreground text-xs">
                <th className="py-1 font-normal">{translate("field")}</th>
                <th className="py-1 font-normal">Omniflow</th>
                <th className="py-1 font-normal">Remnawave</th>
              </tr>
            </thead>
            <tbody>
              {data.fields.map((field) => (
                <tr
                  className={field.matches ? "" : "bg-warning/8"}
                  data-mismatch={!field.matches}
                  key={field.field}
                >
                  <td className="py-1 pr-2">
                    {translate(`fields.${field.field}`)}
                    {!field.matches && <span className="sr-only"> {translate("differs")}</span>}
                  </td>
                  <td className="py-1 pr-2">{display(field, field.omniflow, "omniflow")}</td>
                  <td className="py-1">{display(field, field.remnawave, "remnawave")}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {can("subscriptions.write") && (
            <div className="flex flex-col gap-2 sm:flex-row">
              <Input
                aria-label={translate("reason")}
                onChange={(event) => setReason(event.target.value)}
                placeholder={translate("reason")}
                value={reason}
              />
              <Button
                disabled={pending || reason.trim().length === 0}
                onClick={() => setConfirming(true)}
                size="sm"
              >
                {translate("apply")}
              </Button>
            </div>
          )}
          {error && <p className="text-danger-foreground text-sm">{error.message}</p>}
          {queued && <p className="text-muted-foreground text-sm">{translate("queued")}</p>}
          <ConfirmDialog
            cancelLabel={translate("cancel")}
            confirmLabel={translate("confirm")}
            description={translate("confirmDescription")}
            onConfirm={() => void apply()}
            onOpenChange={setConfirming}
            open={confirming}
            pending={pending}
            title={translate("confirmTitle")}
          />
        </>
      )}
    </div>
  );
}
