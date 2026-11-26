"use client";

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
import { Radio } from "@omniflow/ui/radio";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";

import { CustomerChip } from "@/components/admin/customer-chip";
import { useSession } from "@/lib/session";

/** The ban reasons, each a message template the API knows. */
export const BAN_REASONS = [
  "multiaccount",
  "sharing",
  "fraud",
  "rules",
  "spam",
  "torrent",
] as const;
export type BanReason = (typeof BAN_REASONS)[number];
const DURATIONS = ["1", "7", "30", "90", "0"] as const;

/** What a ban asks the API to do. */
export type Ban = {
  reason: BanReason;
  days: number;
  notify: boolean;
  annulSubscriptions: boolean;
};

/** One account the dialog may ban; `note` is shown beside it. */
export type BanAccount = { id: string; note?: string; selected: boolean };

/**
 * The one way the panel bans a customer from a review screen: which
 * accounts (when there are several), a ready reason, how long, whether the
 * customer is told the reason, and whether their subscriptions are disabled.
 *
 * The reason doubles as the message the customer receives, so it is chosen
 * from the ban templates rather than typed.
 */
export function BanDialog({
  open,
  onOpenChange,
  title,
  description,
  accounts,
  defaultReason = "multiaccount",
  pending = false,
  error,
  onSubmit,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  /** Omitted when the subject is one customer already named by the screen. */
  accounts?: BanAccount[];
  defaultReason?: BanReason;
  pending?: boolean;
  error?: string | null;
  onSubmit: (ban: Ban, customerIds: string[]) => void | Promise<void>;
}) {
  return (
    <Dialog onOpenChange={onOpenChange} open={open}>
      {open && (
        <BanForm
          accounts={accounts}
          defaultReason={defaultReason}
          description={description}
          error={error}
          onSubmit={onSubmit}
          pending={pending}
          title={title}
        />
      )}
    </Dialog>
  );
}

function BanForm({
  title,
  description,
  accounts,
  defaultReason,
  pending,
  error,
  onSubmit,
}: {
  title: string;
  description?: string;
  accounts?: BanAccount[];
  defaultReason: BanReason;
  pending: boolean;
  error?: string | null;
  onSubmit: (ban: Ban, customerIds: string[]) => void | Promise<void>;
}) {
  const translate = useTranslations("admin.banDialog");
  const reasons = useTranslations("admin.moderation.ban.templates");
  const { can } = useSession();
  const name = useId();
  const ids = { duration: useId(), notify: useId(), annul: useId() };
  const [selected, setSelected] = useState<Set<string>>(
    () =>
      new Set((accounts ?? []).filter((account) => account.selected).map((account) => account.id)),
  );
  const [reason, setReason] = useState<BanReason>(defaultReason);
  const [duration, setDuration] = useState<string>("0");
  const [notify, setNotify] = useState(true);
  const [annul, setAnnul] = useState(false);
  const canAnnul = can("subscriptions.write");
  const count = accounts ? selected.size : 1;

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

  return (
    <DialogContent className="sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        {description && <DialogDescription>{description}</DialogDescription>}
      </DialogHeader>

      <form
        className="flex flex-col gap-4"
        onSubmit={(event) => {
          event.preventDefault();
          if (count > 0 && !pending) {
            void onSubmit({ annulSubscriptions: annul, days: Number(duration), notify, reason }, [
              ...selected,
            ]);
          }
        }}
      >
        {accounts && (
          <fieldset className="flex flex-col gap-2">
            <legend className="mb-1 font-medium text-sm">{translate("accounts")}</legend>
            {accounts.map((account) => {
              const id = `${name}-account-${account.id}`;
              return (
                <div className="flex items-center gap-2" key={account.id}>
                  <Checkbox
                    checked={selected.has(account.id)}
                    id={id}
                    onCheckedChange={(checked) => toggle(account.id, checked === true)}
                  />
                  <Label className="flex flex-1 items-center justify-between gap-2" htmlFor={id}>
                    <CustomerChip id={account.id} />
                    {account.note && (
                      <span className="text-muted-foreground text-xs">{account.note}</span>
                    )}
                  </Label>
                </div>
              );
            })}
          </fieldset>
        )}

        <fieldset className="flex flex-col gap-1.5">
          <legend className="mb-1.5 font-medium text-sm">{translate("reason")}</legend>
          {BAN_REASONS.map((value) => {
            const id = `${name}-reason-${value}`;
            return (
              <Label
                className="flex cursor-pointer items-center gap-2.5 rounded-md border px-3 py-2 font-normal has-[:checked]:border-primary has-[:checked]:bg-primary/5"
                htmlFor={id}
                key={value}
              >
                <Radio
                  checked={reason === value}
                  id={id}
                  name={`${name}-reason`}
                  onChange={() => setReason(value)}
                />
                {reasons(value)}
              </Label>
            );
          })}
        </fieldset>

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

        {error && (
          <p className="text-danger-foreground text-xs" role="alert">
            {error}
          </p>
        )}

        <DialogFooter>
          <Button disabled={pending || count === 0} type="submit" variant="destructive">
            {translate("submit", { count })}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  );
}
