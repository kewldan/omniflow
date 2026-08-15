"use client";

import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { toast } from "@omniflow/ui/toast";
import { RefreshCw, TriangleAlert } from "lucide-react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import {
  type AccountSubscription,
  SubscriptionStatus,
  TrafficMeter,
} from "@/components/account/subscription-card";
import { useAccount } from "@/lib/account-session";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";

/**
 * One subscription in detail: what it is, what it is called, and the one
 * destructive control that belongs to it.
 */
export default function SubscriptionPage() {
  const translate = useTranslations("account");
  const params = useParams<{ subscriptionId: string }>();
  const key = `/v1/account/subscriptions/${params.subscriptionId}`;
  const page = `/account/subscriptions/${params.subscriptionId}`;
  const { data, error, isLoading, mutate } = useSWR<AccountSubscription, ApiError>(key, fetcher);

  if (isLoading) {
    return <ListSkeleton rows={2} />;
  }
  if (error || !data) {
    return (
      <AccountNotice
        description={translate("states.errorDescription")}
        title={translate("states.error")}
        variant="danger"
      />
    );
  }

  return (
    <div className="animate-step-in space-y-5">
      <section className="space-y-4 rounded-lg border border-border bg-card p-4">
        <div className="flex items-baseline justify-between gap-3">
          <h1 className="font-semibold text-[19px] tracking-[-0.02em]">{data.label}</h1>
          <span className="font-mono text-[11px] text-subtle-foreground">{data.plan}</span>
        </div>
        <SubscriptionStatus subscription={data} />
        <TrafficMeter traffic={data.traffic} />
      </section>

      <Button asChild className="w-full" size="lg">
        <Link href={`${page}/connect`}>{translate("subscription.connect")}</Link>
      </Button>

      <RenameForm current={data.label} onRenamed={mutate} subscriptionId={data.id} />
      <RotateLink subscriptionId={data.id} />
    </div>
  );
}

/** The customer's own name for a subscription, which every screen then uses. */
function RenameForm({
  current,
  onRenamed,
  subscriptionId,
}: {
  current: string;
  onRenamed: () => void;
  subscriptionId: string;
}) {
  const translate = useTranslations("account");
  const [label, setLabel] = useState(current);
  const [busy, setBusy] = useState(false);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    try {
      await apiFetch(`/v1/account/subscriptions/${subscriptionId}`, {
        body: JSON.stringify({ label }),
        method: "PATCH",
      });
      onRenamed();
      toast.success(translate("subscription.renamed"));
    } catch (renameError) {
      toast.error((renameError as ApiError).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="space-y-3 rounded-lg border border-border bg-card p-4" onSubmit={submit}>
      <Label htmlFor="subscription-label">{translate("subscription.name")}</Label>
      <Input
        id="subscription-label"
        maxLength={40}
        onChange={(event) => setLabel(event.target.value)}
        required
        value={label}
      />
      <Button disabled={busy || label.trim() === current} size="sm" type="submit">
        {translate("actions.save")}
      </Button>
    </form>
  );
}

/**
 * Link rotation.
 *
 * This is the most destructive thing a customer can do to a working setup, so it
 * is behind a typed confirmation as well as a dialog, and the copy says plainly
 * what breaks. It is also the only remedy when a link has leaked, which is why
 * it is here and not hidden in support.
 */
function RotateLink({ subscriptionId }: { subscriptionId: string }) {
  const translate = useTranslations("account");
  const { session } = useAccount();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [rotated, setRotated] = useState<string | null>(null);

  const needsReauth = session?.session.reauthenticationRequired ?? false;

  async function rotate() {
    setBusy(true);
    try {
      const result = await apiFetch<{ subscriptionUrl: string }>(
        `/v1/account/subscriptions/${subscriptionId}/rotate-link`,
        { body: JSON.stringify({ confirm: true }), method: "POST" },
      );
      setRotated(result.subscriptionUrl);
      toast.success(translate("subscription.rotated"));
    } catch (rotateError) {
      const problem = rotateError as ApiError;
      toast.error(
        problem.code === "reauthentication_required"
          ? translate("states.reauthenticate")
          : problem.message,
      );
    } finally {
      setBusy(false);
      setOpen(false);
    }
  }

  return (
    <section className="space-y-3">
      <SectionLabel>{translate("subscription.security")}</SectionLabel>
      <div className="flex items-start gap-3 rounded-xl border border-destructive/40 bg-card p-4">
        <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-destructive/10">
          <TriangleAlert aria-hidden className="size-[18px] text-destructive" />
        </span>
        <p className="text-[13px] text-muted-foreground leading-relaxed">
          {translate("subscription.rotateWarning")}
        </p>
      </div>

      {/* The stale-session case is explained before the button is pressed, so the
          customer is not sent through a confirmation only to be refused. */}
      {needsReauth && (
        <p className="px-1 font-mono text-[11px] text-warning" role="status">
          {translate("states.reauthenticate")}
        </p>
      )}

      <Button
        className="w-full text-destructive"
        disabled={busy}
        onClick={() => setOpen(true)}
        size="lg"
        variant="outline"
      >
        <RefreshCw aria-hidden />
        {translate("subscription.rotate")}
      </Button>

      {rotated && (
        <p className="rounded-lg border border-border bg-card p-4 text-[12.5px] leading-relaxed">
          {translate("subscription.rotatedHint")}
        </p>
      )}

      <ConfirmDialog
        cancelLabel={translate("actions.cancel")}
        confirmationPhrase={translate("subscription.rotatePhrase")}
        confirmationPrompt={translate("subscription.rotatePrompt")}
        confirmLabel={translate("subscription.rotate")}
        description={translate("subscription.rotateWarning")}
        destructive
        onConfirm={rotate}
        onOpenChange={setOpen}
        open={open}
        pending={busy}
        title={translate("subscription.rotate")}
      />
    </section>
  );
}
