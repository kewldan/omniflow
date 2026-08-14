"use client";

import { Alert, AlertDescription, AlertTitle } from "@omniflow/ui/alert";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { toast } from "@omniflow/ui/toast";
import { ArrowRight } from "lucide-react";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import type { MergePreview, MergeSide } from "@/lib/operations";
import { formatMoney, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

/**
 * Merging this account into another one.
 *
 * The case it exists for: a customer bought in Telegram, later signed in on the
 * web through a provider the first account never carried, and now holds an empty
 * second account with their subscription on the other one.
 *
 * The screen is a preview first and an action second, because a merge cannot be
 * undone. Everything that will move is counted before anything moves, and every
 * reason it cannot happen is listed rather than discovered by pressing the
 * button — an operator who has to guess what a merge will do will eventually
 * guess wrong, and the guess costs somebody their subscription.
 *
 * The account on this page is the one being **absorbed**. That direction is
 * fixed and stated, because "merge A and B" is ambiguous in exactly the way that
 * matters.
 */
export function MergePanel({ customerId }: { customerId: string }) {
  const translate = useTranslations("admin.merge");
  const locale = useLocale();
  const { can } = useSession();
  const { run, pending } = useOperatorAction();

  const [into, setInto] = useState("");
  const [reason, setReason] = useState("");
  const [confirming, setConfirming] = useState(false);
  const intoId = useId();
  const reasonId = useId();

  const { data, error, mutate } = useSWR<MergePreview, ApiError>(
    into.trim().length === 36
      ? `/v1/panel/customers/${customerId}/merge/preview?into=${encodeURIComponent(into.trim())}`
      : null,
    fetcher,
  );

  if (!can("customers.write")) {
    return null;
  }

  const blocked = (data?.blockers ?? []).length > 0;

  async function merge() {
    if (
      await run(`/v1/panel/customers/${customerId}/merge`, {
        body: { into: into.trim() },
        method: "POST",
        reason: reason.trim(),
      })
    ) {
      setConfirming(false);
      toast.success(translate("done"));
      mutate();
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="flex flex-col gap-2">
            <Label htmlFor={intoId}>{translate("into")}</Label>
            <Input
              className="font-mono text-xs"
              id={intoId}
              onChange={(event) => setInto(event.target.value)}
              placeholder={translate("intoPlaceholder")}
              value={into}
            />
            <p className="text-subtle-foreground text-xs">{translate("intoHint")}</p>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor={reasonId}>{translate("reason")}</Label>
            <Input
              id={reasonId}
              onChange={(event) => setReason(event.target.value)}
              value={reason}
            />
          </div>
        </div>

        {error ? (
          <Alert variant="danger">
            <AlertTitle>{translate("previewFailed")}</AlertTitle>
            <AlertDescription>{error.message}</AlertDescription>
          </Alert>
        ) : null}

        {data ? (
          <>
            {blocked ? (
              <Alert variant="danger">
                <AlertTitle>{translate("blocked")}</AlertTitle>
                <AlertDescription>
                  <ul className="space-y-1">
                    {data.blockers.map((blocker) => (
                      <li key={blocker}>{translate(`blocker.${blocker}`)}</li>
                    ))}
                  </ul>
                </AlertDescription>
              </Alert>
            ) : null}

            <div className="grid gap-4 sm:grid-cols-[1fr_auto_1fr]">
              <SideCard label={translate("absorbed")} locale={locale} side={data.source} />
              <div className="flex items-center justify-center">
                <ArrowRight aria-hidden className="size-5 text-subtle-foreground" />
              </div>
              <SideCard label={translate("survives")} locale={locale} side={data.target} />
            </div>

            {data.notes.length > 0 && !blocked ? (
              <Alert variant="warning">
                <AlertTitle>{translate("notesTitle")}</AlertTitle>
                <AlertDescription>
                  <ul className="space-y-1">
                    {data.notes.map((note) => (
                      <li key={note}>{translate(`note.${note}`)}</li>
                    ))}
                  </ul>
                </AlertDescription>
              </Alert>
            ) : null}

            <div>
              <Button
                disabled={blocked || pending || reason.trim().length === 0}
                onClick={() => setConfirming(true)}
                variant="destructive"
              >
                {translate("merge")}
              </Button>
            </div>
          </>
        ) : null}

        {/* Typing the identifier of the account being absorbed, because this is
            the one operator action in the panel that cannot be reversed and
            cannot be partially applied. */}
        <ConfirmDialog
          cancelLabel={translate("cancel")}
          confirmationPhrase={customerId}
          confirmationPrompt={translate("confirmPrompt")}
          confirmLabel={translate("merge")}
          description={translate("confirmWarning")}
          destructive
          onConfirm={merge}
          onOpenChange={setConfirming}
          open={confirming}
          pending={pending}
          title={translate("confirmTitle")}
        />
      </CardContent>
    </Card>
  );
}

function SideCard({ label, locale, side }: { label: string; locale: string; side: MergeSide }) {
  const translate = useTranslations("admin.merge");
  return (
    <div className="space-y-2 rounded-lg border border-border p-3">
      <p className="font-mono text-[11px] text-subtle-foreground">{label}</p>
      <p className="truncate font-mono text-xs">{side.id}</p>
      <dl className="space-y-1 text-xs">
        <Line label={translate("counts.subscriptions")} value={side.activeSubscriptions} />
        <Line label={translate("counts.orders")} value={side.orders} />
        <Line label={translate("counts.identities")} value={side.identities} />
        <Line label={translate("counts.tickets")} value={side.tickets} />
        <Line label={translate("counts.referrals")} value={side.referralsMade} />
      </dl>
      {side.wallet.length > 0 ? (
        <div className="border-border border-t pt-2">
          {side.wallet.map((balance) => (
            <p className="font-medium text-xs tabular-nums" key={balance.currency}>
              {formatMoney(balance.balanceMinor, balance.currency, locale)}
            </p>
          ))}
        </div>
      ) : (
        <p className="text-subtle-foreground text-xs">{translate("counts.noWallet")}</p>
      )}
    </div>
  );
}

function Line({ label, value }: { label: string; value: number }) {
  return (
    <div className="flex items-baseline justify-between gap-2">
      <dt className="text-subtle-foreground">{label}</dt>
      <dd className="tabular-nums">{value}</dd>
    </div>
  );
}
