"use client";

import type { PanelSharedDevice } from "@omniflow/api-client/generated";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";

import { BanDialog } from "@/components/admin/ban-dialog";
import { useOperatorAction } from "@/lib/operations";

/**
 * Banning from a shared device. Every account but the one first seen on the
 * device is ticked to begin with: the first is usually the original and the
 * rest the second accounts. The operator changes that when the evidence says
 * otherwise.
 */
export function BanDeviceDialog({
  device,
  onBanned,
  onOpenChange,
}: {
  device: PanelSharedDevice | null;
  onBanned: () => void;
  onOpenChange: (open: boolean) => void;
}) {
  const translate = useTranslations("admin.risk.sharedDevices.ban");
  const { run, pending, error } = useOperatorAction();
  const firstSeen = device
    ? [...device.customers].sort((left, right) =>
        left.firstSeenAt.localeCompare(right.firstSeenAt),
      )[0]?.customerId
    : undefined;

  return (
    <BanDialog
      accounts={device?.customers.map((account) => ({
        id: account.customerId,
        note: account.customerId === firstSeen ? translate("firstSeen") : undefined,
        selected: account.customerId !== firstSeen,
      }))}
      description={translate("description")}
      error={error?.message}
      key={device?.handle}
      onOpenChange={onOpenChange}
      onSubmit={async (ban, customerIds) => {
        if (!device) {
          return;
        }
        const ok = await run(`/v1/panel/risk/shared-devices/${device.handle}/ban`, {
          body: { ...ban, customerIds },
          method: "POST",
        });
        if (ok) {
          toast.success(translate("done", { count: customerIds.length }));
          onBanned();
        }
      }}
      open={device !== null}
      pending={pending}
      title={translate("title")}
    />
  );
}
