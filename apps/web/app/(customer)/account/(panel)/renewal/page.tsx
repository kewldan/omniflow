"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@omniflow/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Switch } from "@omniflow/ui/switch";
import { toast } from "@omniflow/ui/toast";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";
import { useProblemMessage } from "@/components/account/commerce/reasons";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { Timestamp } from "@/components/intl/timestamp";
import { apiFetch, fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";
import { providerKey } from "@/lib/payment-providers";

type SavedMethod = {
  id: string;
  provider: string;
  label: string;
  status: "active" | "expired" | "failed" | "revoked";
  isDefault: boolean;
  createdAt: string;
};

type SubscriptionRenewal = {
  subscriptionId: string;
  label: string;
  planName: string;
  endsAt?: string;
  supported: boolean;
  enabled: boolean;
  funding: "wallet" | "saved_method";
  leadTimeHours: number;
  state: "idle" | "scheduled" | "dunning" | "suspended";
  methodLabel: string;
  periodDays: number;
  currency: string;
  periods: RenewalPeriod[];
};

type RenewalPeriod = {
  days: number;
  amountMinor: number;
  own: boolean;
};

type Renewal = {
  available: boolean;
  methods: SavedMethod[];
  subscriptions: SubscriptionRenewal[];
};

/** The reasons a customer can give for switching renewal off, in display order. */
const CHURN_REASONS = [
  "price",
  "blocked",
  "speed",
  "not_needed",
  "switched",
  "technical",
  "other",
] as const;

/** Lead times a customer can pick, in hours before the period ends. */
const LEAD_TIMES = [24, 48, 72, 120, 168];

const ENDPOINT = "/v1/account/renewal";

/**
 * Automatic renewal and saved cards.
 *
 * The bot has always had these screens; this is the same setting, read and
 * written through the same rows. Turning renewal on is the customer's consent
 * and is recorded as such by the server. A saved card is only ever a masked
 * label the payment provider supplied — the panel never sees a number.
 */
export default function RenewalPage() {
  const translate = useTranslations("account.commerce.renewal");
  const describeProblem = useProblemMessage();
  const money = useMoney();
  const { data, error, isLoading, mutate } = useSWR<Renewal>(ENDPOINT, fetcher);
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState<SavedMethod | null>(null);
  // The subscription whose renewal was just switched off, while the customer
  // is asked why. Answering is optional and never blocks the switch itself.
  const [leaving, setLeaving] = useState<string | null>(null);

  async function update(path: string, init: RequestInit): Promise<boolean> {
    setBusy(true);
    try {
      const next = await apiFetch<Renewal>(path, init);
      await mutate(next, { revalidate: false });
      toast.success(translate("saved"));
      return true;
    } catch (failure) {
      toast.error(describeProblem(failure));
      return false;
    } finally {
      setBusy(false);
    }
  }

  async function change(subscriptionId: string, body: Record<string, unknown>) {
    const saved = await update(`${ENDPOINT}/${subscriptionId}`, {
      body: JSON.stringify(body),
      method: "PUT",
    });
    if (saved && body.enabled === false) {
      setLeaving(subscriptionId);
    }
  }

  async function answer(reason: (typeof CHURN_REASONS)[number]) {
    const subscriptionId = leaving;
    setLeaving(null);
    try {
      await apiFetch<void>("/v1/account/churn-feedback", {
        body: JSON.stringify({ occasion: "auto_renew_off", reason, subscriptionId }),
        method: "POST",
      });
      toast.success(translate("churn.thanks"));
    } catch {
      // The answer is a courtesy; a failure to store it is not the
      // customer's problem and is not worth an error.
    }
  }

  if (isLoading) {
    return <ListSkeleton rows={3} />;
  }
  if (error || !data) {
    return <AccountNotice title={translate("loadFailed")} variant="danger" />;
  }

  const activeMethods = data.methods.filter((method) => method.status === "active");

  return (
    <div className="space-y-8">
      <p className="text-[13.5px] text-muted-foreground">{translate("description")}</p>

      {!data.available && <AccountNotice title={translate("unavailable")} variant="empty" />}

      <section className="space-y-3">
        <SectionLabel>{translate("subscriptionsTitle")}</SectionLabel>
        {data.subscriptions.length === 0 ? (
          <p className="text-[13.5px] text-muted-foreground">{translate("noSubscriptions")}</p>
        ) : (
          <ul className="space-y-3">
            {data.subscriptions.map((entry) => (
              <li
                className="space-y-3 rounded-lg border border-border bg-card p-4"
                key={entry.subscriptionId}
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <p className="truncate font-medium text-[14px]">
                      {entry.label || entry.planName}
                    </p>
                    {entry.endsAt && (
                      <p className="text-[12.5px] text-muted-foreground">
                        {translate("endsAt")} <Timestamp value={entry.endsAt} />
                      </p>
                    )}
                    {entry.state === "dunning" && (
                      <Badge className="mt-1" variant="outline">
                        {translate("state.dunning")}
                      </Badge>
                    )}
                    {entry.state === "suspended" && entry.enabled && (
                      <Badge className="mt-1" variant="outline">
                        {translate("state.suspended")}
                      </Badge>
                    )}
                  </div>
                  <Switch
                    aria-label={translate("toggle")}
                    checked={entry.enabled}
                    disabled={busy || (!entry.supported && !entry.enabled)}
                    onCheckedChange={(checked) =>
                      change(entry.subscriptionId, { enabled: checked })
                    }
                  />
                </div>
                {!entry.supported && !entry.enabled && (
                  <p className="text-[12.5px] text-muted-foreground">{translate("notSupported")}</p>
                )}
                {entry.enabled && (
                  <div className="grid gap-3 sm:grid-cols-2">
                    <div className="space-y-1">
                      <p className="text-[12.5px] text-muted-foreground">{translate("funding")}</p>
                      <Select
                        disabled={busy}
                        onValueChange={(funding) => change(entry.subscriptionId, { funding })}
                        value={entry.funding}
                      >
                        <SelectTrigger aria-label={translate("funding")}>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="wallet">{translate("fundingWallet")}</SelectItem>
                          <SelectItem disabled={activeMethods.length === 0} value="saved_method">
                            {entry.methodLabel
                              ? translate("fundingMethodLabel", { label: entry.methodLabel })
                              : translate("fundingMethod")}
                          </SelectItem>
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="space-y-1">
                      <p className="text-[12.5px] text-muted-foreground">{translate("leadTime")}</p>
                      <Select
                        disabled={busy}
                        onValueChange={(hours) =>
                          change(entry.subscriptionId, { leadTimeHours: Number(hours) })
                        }
                        value={String(entry.leadTimeHours)}
                      >
                        <SelectTrigger aria-label={translate("leadTime")}>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {(LEAD_TIMES.includes(entry.leadTimeHours)
                            ? LEAD_TIMES
                            : [...LEAD_TIMES, entry.leadTimeHours].sort((a, b) => a - b)
                          ).map((hours) => (
                            <SelectItem key={hours} value={String(hours)}>
                              {translate("leadTimeDays", { days: Math.round(hours / 24) })}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    {entry.periods.length > 1 && (
                      <div className="space-y-1 sm:col-span-2">
                        <p className="text-[12.5px] text-muted-foreground">{translate("period")}</p>
                        <Select
                          disabled={busy}
                          onValueChange={(days) =>
                            change(entry.subscriptionId, { periodDays: Number(days) })
                          }
                          value={String(entry.periodDays)}
                        >
                          <SelectTrigger aria-label={translate("period")}>
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="0">{translate("periodRepeat")}</SelectItem>
                            {entry.periods.map((period) => (
                              <SelectItem key={period.days} value={String(period.days)}>
                                {translate("periodOption", {
                                  days: period.days,
                                  price: money(period.amountMinor, entry.currency),
                                })}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      </div>
                    )}
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="space-y-3">
        <SectionLabel>{translate("methodsTitle")}</SectionLabel>
        {data.methods.length === 0 ? (
          <p className="text-[13.5px] text-muted-foreground">{translate("noMethods")}</p>
        ) : (
          <ul className="space-y-2">
            {data.methods.map((method) => (
              <li
                className="flex flex-wrap items-center gap-3 rounded-lg border border-border bg-card p-3"
                key={method.id}
              >
                <div className="min-w-0 flex-1">
                  <p className="truncate font-medium text-[13.5px]">
                    {method.label ||
                      translate("unnamedMethod", {
                        provider: providerKey(method.provider),
                      })}
                  </p>
                  <p className="text-[12.5px] text-muted-foreground">
                    {translate("addedAt")} <Timestamp value={method.createdAt} />
                  </p>
                </div>
                {method.isDefault && <Badge variant="solid">{translate("default")}</Badge>}
                {method.status !== "active" && (
                  <Badge variant="outline">{translate(`methodStatus.${method.status}`)}</Badge>
                )}
                {!method.isDefault && method.status === "active" && (
                  <Button
                    disabled={busy}
                    onClick={() =>
                      update(`/v1/account/payment-methods/${method.id}/default`, { method: "POST" })
                    }
                    size="sm"
                    variant="outline"
                  >
                    {translate("makeDefault")}
                  </Button>
                )}
                <Button
                  disabled={busy}
                  onClick={() => setRemoving(method)}
                  size="sm"
                  variant="ghost"
                >
                  {translate("remove")}
                </Button>
              </li>
            ))}
          </ul>
        )}
        <p className="text-[12.5px] text-muted-foreground">{translate("methodsNotice")}</p>
        <p className="text-[12.5px] text-muted-foreground">
          <Link className="underline underline-offset-2" href="/pages/recurring-terms">
            {translate("termsLink")}
          </Link>
        </p>
      </section>

      <Dialog
        onOpenChange={(open) => {
          if (!open) {
            setLeaving(null);
          }
        }}
        open={leaving !== null}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{translate("churn.title")}</DialogTitle>
            <DialogDescription>{translate("churn.description")}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-2 sm:grid-cols-2">
            {CHURN_REASONS.map((reason) => (
              <Button key={reason} onClick={() => void answer(reason)} variant="secondary">
                {translate(`churn.reasons.${reason}`)}
              </Button>
            ))}
          </div>
          <DialogFooter>
            <Button onClick={() => setLeaving(null)} variant="ghost">
              {translate("churn.skip")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        cancelLabel={translate("cancel")}
        confirmLabel={translate("remove")}
        destructive
        description={translate("removeDescription")}
        onConfirm={async () => {
          if (removing) {
            await update(`/v1/account/payment-methods/${removing.id}`, { method: "DELETE" });
          }
          setRemoving(null);
        }}
        onOpenChange={(open) => {
          if (!open) {
            setRemoving(null);
          }
        }}
        open={removing !== null}
        title={translate("removeTitle")}
      />
    </div>
  );
}
