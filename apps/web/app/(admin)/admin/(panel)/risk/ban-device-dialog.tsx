"use client";

import type { PanelSharedDevice } from "@omniflow/api-client/generated";
import { Button } from "@omniflow/ui/button";
import { Checkbox } from "@omniflow/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@omniflow/ui/dialog";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";

import { CustomerChip } from "@/components/admin/customer-chip";
import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

const REASONS = ["multiaccount", "sharing", "fraud", "rules"] as const;
const DURATIONS = ["1", "7", "30", "90", "0"] as const;

/**
 * Banning from a shared device: which accounts, why, for how long, whether
 * they are told, and whether their subscriptions are disabled too.
 *
 * Every account but the one first seen on the device is ticked to begin with:
 * the first is usually the original and the rest the second accounts. The
 * operator changes that when the evidence says otherwise.
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
  return (
    <Dialog onOpenChange={onOpenChange} open={device !== null}>
      {device && <BanForm device={device} key={device.handle} onBanned={onBanned} />}
    </Dialog>
  );
}

function BanForm({ device, onBanned }: { device: PanelSharedDevice; onBanned: () => void }) {
  const translate = useTranslations("admin.risk.sharedDevices.ban");
  const reasons = useTranslations("admin.moderation.ban.templates");
  const { can } = useSession();
  const { run, pending, error } = useOperatorAction();
  const ids = { reason: useId(), duration: useId(), notify: useId(), annul: useId() };

  const firstSeen = [...device.customers].sort((left, right) =>
    left.firstSeenAt.localeCompare(right.firstSeenAt),
  )[0]?.customerId;
  const [selected, setSelected] = useState<Set<string>>(
    () =>
      new Set(
        device.customers.map((account) => account.customerId).filter((id) => id !== firstSeen),
      ),
  );
  const [reason, setReason] = useState<string>("multiaccount");
  const [duration, setDuration] = useState<string>("0");
  const [notify, setNotify] = useState(true);
  const [annul, setAnnul] = useState(false);
  const canAnnul = can("subscriptions.write");

  function toggle(customerId: string, checked: boolean) {
    setSelected((current) => {
      const next = new Set(current);
      if (checked) {
        next.add(customerId);
      } else {
        next.delete(customerId);
      }
      return next;
    });
  }

  async function submit() {
    const ok = await run(`/v1/panel/risk/shared-devices/${device.handle}/ban`, {
      body: {
        annulSubscriptions: annul,
        customerIds: [...selected],
        days: Number(duration),
        notify,
        reason,
      },
      method: "POST",
    });
    if (ok) {
      toast.success(translate("done", { count: selected.size }));
      onBanned();
    }
  }

  return (
    <DialogContent className="sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>{translate("title")}</DialogTitle>
        <DialogDescription>{translate("description")}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <fieldset className="flex flex-col gap-2">
          <legend className="mb-1 font-medium text-sm">{translate("accounts")}</legend>
          {device.customers.map((account) => {
            const id = `ban-${device.handle}-${account.customerId}`;
            return (
              <div className="flex items-center gap-2" key={account.customerId}>
                <Checkbox
                  checked={selected.has(account.customerId)}
                  id={id}
                  onCheckedChange={(checked) => toggle(account.customerId, checked === true)}
                />
                <Label className="flex flex-1 items-center justify-between gap-2" htmlFor={id}>
                  <CustomerChip id={account.customerId} />
                  {account.customerId === firstSeen && (
                    <span className="text-muted-foreground text-xs">{translate("firstSeen")}</span>
                  )}
                </Label>
              </div>
            );
          })}
        </fieldset>

        <div className="grid gap-3 sm:grid-cols-2">
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.reason}>{translate("reason")}</Label>
            <Select onValueChange={setReason} value={reason}>
              <SelectTrigger id={ids.reason}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {REASONS.map((value) => (
                  <SelectItem key={value} value={value}>
                    {reasons(value)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor={ids.duration}>{translate("duration")}</Label>
            <Select onValueChange={setDuration} value={duration}>
              <SelectTrigger id={ids.duration}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {DURATIONS.map((value) => (
                  <SelectItem key={value} value={value}>
                    {value === "0"
                      ? translate("forever")
                      : translate("days", { count: Number(value) })}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>

        <div className="flex items-start gap-2">
          <Checkbox
            checked={notify}
            id={ids.notify}
            onCheckedChange={(checked) => setNotify(checked === true)}
          />
          <Label className="font-normal" htmlFor={ids.notify}>
            {translate("notify")}
          </Label>
        </div>
        <div className="flex items-start gap-2">
          <Checkbox
            checked={annul}
            disabled={!canAnnul}
            id={ids.annul}
            onCheckedChange={(checked) => setAnnul(checked === true)}
          />
          <div className="flex flex-col gap-0.5">
            <Label className="font-normal" htmlFor={ids.annul}>
              {translate("annul")}
            </Label>
            <span className="text-muted-foreground text-xs">
              {canAnnul ? translate("annulHint") : translate("annulForbidden")}
            </span>
          </div>
        </div>

        {error && <p className="text-danger-foreground text-xs">{error.message}</p>}
      </div>

      <DialogFooter>
        <Button disabled={pending || selected.size === 0} onClick={submit} variant="destructive">
          {translate("submit", { count: selected.size })}
        </Button>
      </DialogFooter>
    </DialogContent>
  );
}
