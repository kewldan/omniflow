"use client";

import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { toast } from "@omniflow/ui/toast";
import { useFormatter, useTranslations } from "next-intl";
import { useId, useState } from "react";
import { useForm } from "react-hook-form";

import { useProblemMessage } from "@/components/account/account/problem";
import type { DeletionState } from "@/components/account/account/types";
import { ReauthNotice } from "@/components/account/reauth";
import { useAccount } from "@/lib/account-session";
import { type ApiError, apiFetch } from "@/lib/api";

// 400 characters is the server's ceiling. Enforcing it here means the customer
// is stopped by a counter rather than by a rejected request after they have
// written a paragraph.
const REASON_LIMIT = 400;

type DeletionValues = { reason: string };

/**
 * The reason field's rules, as React Hook Form validators.
 *
 * A schema validator was the first implementation, and Zod belongs at a trust
 * boundary — but the boundary here is the API, which enforces the same ceiling
 * and owns the refusal. Shipping one to the browser for a single text field is
 * weight a customer's phone pays for nothing.
 */
const REASON_RULES = {
  maxLength: REASON_LIMIT,
  validate: (value: string) => value.trim().length > 0,
} as const;

/**
 * The account-deletion request.
 *
 * The single most important thing this component does is refuse to pretend. It
 * does not delete an account; it records a request that an operator's retention
 * workflow carries out later, and every piece of copy around it says so. A
 * screen that implied the account vanished on click would leave a customer
 * believing their data was gone while it demonstrably was not — and would make
 * the cancel control, which is the whole point of the waiting period,
 * incomprehensible.
 */
export function DeletionRequest({
  deletion,
  onChanged,
}: {
  deletion: DeletionState;
  onChanged: () => void;
}) {
  const translate = useTranslations("account.account");
  const format = useFormatter();
  const describeProblem = useProblemMessage();
  const { session } = useAccount();
  const reasonId = useId();

  const [confirming, setConfirming] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  // Set when the server refused for want of a recent sign-in. It is kept apart
  // from the general failure because the remedy is a link, not a retry.
  const [needsReauth, setNeedsReauth] = useState(false);

  const form = useForm<DeletionValues>({
    defaultValues: { reason: "" },
  });
  const reason = form.watch("reason");

  // The panel already knows the session is too old for a destructive action, so
  // it says so before the attempt rather than after a refusal.
  const staleSession = needsReauth || (session?.session.reauthenticationRequired ?? false);

  async function request(values: DeletionValues) {
    setBusy(true);
    setFailure(null);
    try {
      await apiFetch("/v1/account/privacy/deletion", {
        // `confirm` is sent explicitly because the server requires it. A form
        // that could be submitted without it would be one mis-routed request
        // away from starting the removal of somebody's account.
        body: JSON.stringify({ confirm: true, reason: values.reason.trim() }),
        method: "POST",
      });
      form.reset({ reason: "" });
      onChanged();
      toast.success(translate("deletion.requested"));
    } catch (requestError) {
      const problem = requestError as ApiError;
      if (problem.code === "reauthentication_required") {
        setNeedsReauth(true);
      }
      setFailure(describeProblem(requestError));
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  }

  async function cancel() {
    setBusy(true);
    setFailure(null);
    try {
      await apiFetch("/v1/account/privacy/deletion", { method: "DELETE" });
      onChanged();
      toast.success(translate("deletion.cancelled"));
    } catch (cancelError) {
      setFailure(describeProblem(cancelError));
    } finally {
      setBusy(false);
      setCancelling(false);
    }
  }

  function moment(value: string | null): string {
    return value
      ? format.dateTime(new Date(value), {
          day: "numeric",
          hour: "2-digit",
          minute: "2-digit",
          month: "long",
          year: "numeric",
        })
      : "";
  }

  if (deletion.pending) {
    return (
      <section className="space-y-3 rounded-xl border border-warning/40 bg-warning/10 p-4">
        <p className="font-semibold text-[15px]">{translate("deletion.pendingTitle")}</p>
        <p className="text-[12.5px] leading-relaxed">
          {translate("deletion.pendingDescription", { date: moment(deletion.requestedAt) })}
        </p>
        <p className="text-[12.5px] leading-relaxed">{translate("deletion.executedBy")}</p>
        {deletion.reason && (
          <p className="rounded-md border border-border bg-card px-3 py-2.5 text-[12.5px] leading-relaxed">
            {translate("deletion.yourReason", { reason: deletion.reason })}
          </p>
        )}

        {failure && (
          <p className="text-[12.5px] text-destructive leading-relaxed" role="alert">
            {failure}
          </p>
        )}

        <Button
          className="w-full"
          disabled={busy}
          onClick={() => setCancelling(true)}
          size="lg"
          variant="outline"
        >
          {translate("deletion.cancel")}
        </Button>

        <ConfirmDialog
          cancelLabel={translate("actions.cancel")}
          confirmLabel={translate("deletion.cancel")}
          description={translate("deletion.cancelDescription")}
          onConfirm={cancel}
          onOpenChange={setCancelling}
          open={cancelling}
          pending={busy}
          title={translate("deletion.cancel")}
        />
      </section>
    );
  }

  return (
    <section className="space-y-3 rounded-xl border border-border bg-card p-4">
      <div>
        <p className="font-medium text-[13.5px]">{translate("deletion.title")}</p>
        <p className="mt-1 text-[12.5px] text-muted-foreground leading-relaxed">
          {translate("deletion.description")}
        </p>
        <p className="mt-1.5 text-[12.5px] text-muted-foreground leading-relaxed">
          {translate("deletion.executedBy")}
        </p>
      </div>

      {deletion.cancelledAt && (
        <p className="font-mono text-[11px] text-subtle-foreground">
          {translate("deletion.previouslyCancelled", { date: moment(deletion.cancelledAt) })}
        </p>
      )}

      <form
        className="space-y-3"
        noValidate
        onSubmit={form.handleSubmit(() => setConfirming(true))}
      >
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={reasonId}>{translate("deletion.reason")}</Label>
          <Textarea
            aria-describedby={`${reasonId}-hint`}
            aria-invalid={Boolean(form.formState.errors.reason)}
            id={reasonId}
            maxLength={REASON_LIMIT}
            placeholder={translate("deletion.reasonPlaceholder")}
            {...form.register("reason", REASON_RULES)}
          />
          <p className="text-[12px] text-subtle-foreground leading-relaxed" id={`${reasonId}-hint`}>
            {form.formState.errors.reason
              ? translate("deletion.reasonRequired")
              : translate("deletion.reasonHint", {
                  remaining: Math.max(0, REASON_LIMIT - reason.length),
                })}
          </p>
        </div>

        {staleSession && <ReauthNotice />}

        {failure && !staleSession && (
          <p className="text-[12.5px] text-destructive leading-relaxed" role="alert">
            {failure}
          </p>
        )}

        <Button
          className="w-full text-destructive"
          disabled={busy}
          size="lg"
          type="submit"
          variant="outline"
        >
          {translate("deletion.request")}
        </Button>
      </form>

      <ConfirmDialog
        cancelLabel={translate("actions.cancel")}
        confirmLabel={translate("deletion.confirm")}
        description={translate("deletion.confirmDescription")}
        destructive
        onConfirm={form.handleSubmit(request)}
        onOpenChange={setConfirming}
        open={confirming}
        pending={busy}
        title={translate("deletion.confirmTitle")}
      />
    </section>
  );
}
