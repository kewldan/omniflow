"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import { type Listing, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

type BulkOperation = {
  id: string;
  kind: string;
  status: string;
  reason: string;
  total: number;
  succeeded: number;
  failed: number;
  skipped: number;
  createdAt: string;
  completedAt?: string;
};

type BulkItem = {
  position: number;
  targetType: string;
  targetId: string;
  status: string;
  errorCode?: string;
  processedAt?: string;
};

const KINDS = [
  "subscription_extend",
  "subscription_enable",
  "subscription_disable",
  "wallet_credit",
] as const;

/**
 * Bulk actions across many customers or subscriptions.
 *
 * Nothing runs until an operator has seen what it will touch. That is enforced
 * by the two-step API rather than by this screen: a preview records the
 * operation and every target without applying anything, and starting it only
 * accepts an operation already in the ready state. There is no path from a form
 * submission to an applied change that skips the preview.
 */
export function BulkActions() {
  const translate = useTranslations("admin.bulk");
  const { can } = useSession();
  const [composing, setComposing] = useState(false);
  const [inspecting, setInspecting] = useState("");

  const { data, isLoading, mutate } = useSWR<Listing<BulkOperation>, ApiError>(
    "/v1/panel/bulk",
    fetcher,
    // A running operation changes while an operator watches it, and the
    // per-item results are the point of watching.
    { refreshInterval: 5000 },
  );
  const items = data?.items ?? [];

  return (
    <div className="flex flex-col gap-4">
      {can("customers.write") && (
        <Button className="self-start" onClick={() => setComposing((open) => !open)} size="sm">
          {translate(composing ? "close" : "create")}
        </Button>
      )}
      {composing && (
        <BulkComposer
          onPreviewed={() => {
            setComposing(false);
            void mutate();
          }}
        />
      )}

      {isLoading ? (
        <Skeleton className="h-32 w-full" />
      ) : items.length === 0 ? (
        <StateNotice
          description={translate("empty.description")}
          title={translate("empty.title")}
          variant="empty"
        />
      ) : (
        items.map((operation) => (
          <Card className="flex flex-col gap-3 p-4" key={operation.id}>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <span className="flex items-center gap-2">
                <span className="font-medium">{translate(`kind.${operation.kind}`)}</span>
                <Badge
                  variant={
                    operation.status === "completed"
                      ? "success"
                      : operation.status === "running"
                        ? "warning"
                        : "neutral"
                  }
                >
                  {translate(`status.${operation.status}`)}
                </Badge>
              </span>
              <span className="flex items-center gap-3 text-sm tabular-nums">
                {/* Four counters rather than a percentage: an operator needs to
                    know how many failed, not how close to done it is. */}
                <span>{translate("counts.total", { count: operation.total })}</span>
                <span className="text-success-foreground">
                  {translate("counts.succeeded", { count: operation.succeeded })}
                </span>
                <span className="text-danger-foreground">
                  {translate("counts.failed", { count: operation.failed })}
                </span>
                <span className="text-muted-foreground">
                  {translate("counts.skipped", { count: operation.skipped })}
                </span>
              </span>
            </div>
            <p className="text-muted-foreground text-sm">{operation.reason}</p>
            <div className="flex flex-wrap gap-2">
              {can("customers.write") && operation.status === "ready" && (
                <StartButton onDone={() => mutate()} operationId={operation.id} />
              )}
              {can("customers.write") &&
                (operation.status === "ready" || operation.status === "running") && (
                  <CancelButton onDone={() => mutate()} operationId={operation.id} />
                )}
              <Button
                onClick={() => setInspecting(inspecting === operation.id ? "" : operation.id)}
                size="sm"
                variant="ghost"
              >
                {translate(inspecting === operation.id ? "hideItems" : "showItems")}
              </Button>
            </div>
            {inspecting === operation.id && <BulkItems operationId={operation.id} />}
          </Card>
        ))
      )}
    </div>
  );
}

/**
 * The per-item results.
 *
 * A bulk action that half succeeds has to say which half. "Extend 400
 * subscriptions" reporting only "failed" is not something anybody can act on,
 * so every target carries its own outcome and its own error class.
 */
function BulkItems({ operationId }: { operationId: string }) {
  const translate = useTranslations("admin.bulk");
  const { data, isLoading } = useSWR<{ items: BulkItem[] }, ApiError>(
    `/v1/panel/bulk/${operationId}/items?pageSize=200`,
    fetcher,
    { refreshInterval: 5000 },
  );

  if (isLoading) {
    return <Skeleton className="h-24 w-full" />;
  }
  const items = data?.items ?? [];

  return (
    <div className="flex max-h-64 flex-col gap-1 overflow-y-auto border-border border-t pt-2">
      {items.map((item) => (
        <div
          className="flex items-baseline justify-between gap-3 text-sm"
          key={`${item.targetType}-${item.position}`}
        >
          <span className="font-mono text-[11px]">{item.targetId.slice(0, 8)}</span>
          <span className="flex items-center gap-2">
            {item.errorCode && (
              <span className="text-danger-foreground text-xs">
                {translate(`errorCode.${item.errorCode}`)}
              </span>
            )}
            <Badge
              variant={
                item.status === "succeeded"
                  ? "success"
                  : item.status === "failed"
                    ? "danger"
                    : "neutral"
              }
            >
              {translate(`itemStatus.${item.status}`)}
            </Badge>
          </span>
        </div>
      ))}
    </div>
  );
}

/**
 * Records a bulk action and its targets without applying anything.
 *
 * The identifiers are pasted rather than selected from the search results,
 * which is deliberate for a first version: an operator assembling a list of
 * four hundred subscriptions has almost certainly produced it from a query
 * elsewhere, and a checkbox column that only works for the current page would
 * be worse than useless for that.
 */
function BulkComposer({ onPreviewed }: { onPreviewed: () => void }) {
  const translate = useTranslations("admin.bulk");
  const kindId = useId();
  const targetsId = useId();
  const daysId = useId();
  const amountId = useId();
  const currencyId = useId();
  const reasonId = useId();

  const [kind, setKind] = useState<string>("subscription_extend");
  const [targets, setTargets] = useState("");
  const [days, setDays] = useState("30");
  const [amountMinor, setAmountMinor] = useState("");
  const [currency, setCurrency] = useState("RUB");
  const [reason, setReason] = useState("");
  const { run, pending, error } = useOperatorAction();

  const identifiers = targets
    .split(/[\s,]+/)
    .map((value) => value.trim())
    .filter((value) => value.length > 0);
  const targetType = kind === "wallet_credit" ? "customer" : "subscription";
  const ready = identifiers.length > 0 && reason.trim().length > 0 && !pending;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("composer.title")}</CardTitle>
        <CardDescription>{translate("composer.description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={kindId}>{translate("composer.kind")}</Label>
          <select
            className="h-9 rounded-md border border-border bg-transparent px-2 text-sm"
            id={kindId}
            onChange={(event) => setKind(event.target.value)}
            value={kind}
          >
            {KINDS.map((option) => (
              <option key={option} value={option}>
                {translate(`kind.${option}`)}
              </option>
            ))}
          </select>
        </div>

        <div className="flex flex-col gap-1.5">
          <Label htmlFor={targetsId}>{translate(`composer.targets.${targetType}`)}</Label>
          <textarea
            className="min-h-24 rounded-md border border-border bg-transparent p-2 font-mono text-xs"
            id={targetsId}
            onChange={(event) => setTargets(event.target.value)}
            value={targets}
          />
          <span className="text-muted-foreground text-xs">
            {translate("composer.targetCount", { count: identifiers.length })}
          </span>
        </div>

        {kind === "subscription_extend" && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={daysId}>{translate("composer.days")}</Label>
            <Input
              id={daysId}
              inputMode="numeric"
              onChange={(event) => setDays(event.target.value)}
              value={days}
            />
          </div>
        )}

        {kind === "wallet_credit" && (
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={amountId}>{translate("composer.amount")}</Label>
              <Input
                id={amountId}
                inputMode="numeric"
                onChange={(event) => setAmountMinor(event.target.value)}
                value={amountMinor}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={currencyId}>{translate("composer.currency")}</Label>
              <Input
                id={currencyId}
                onChange={(event) => setCurrency(event.target.value.toUpperCase())}
                value={currency}
              />
            </div>
          </div>
        )}

        <div className="flex flex-col gap-1.5">
          <Label htmlFor={reasonId}>{translate("composer.reason")}</Label>
          <Input
            id={reasonId}
            onChange={(event) => setReason(event.target.value)}
            placeholder={translate("composer.reasonHint")}
            value={reason}
          />
        </div>

        <p className="text-muted-foreground text-xs">{translate("composer.previewNotice")}</p>
        {error && <p className="text-danger-foreground text-sm">{error.message}</p>}
        <Button
          className="self-start"
          disabled={!ready}
          onClick={async () => {
            const ok = await run("/v1/panel/bulk", {
              body: {
                kind,
                parameters:
                  kind === "subscription_extend"
                    ? { days: Number(days) || 0 }
                    : kind === "wallet_credit"
                      ? { amountMinor: Number(amountMinor) || 0, currency }
                      : {},
                targets: identifiers.map((id) => ({ id, type: targetType })),
              },
              method: "POST",
              reason: reason.trim(),
            });
            if (ok) {
              setTargets("");
              setReason("");
              onPreviewed();
            }
          }}
          size="sm"
        >
          {translate("composer.preview")}
        </Button>
      </CardContent>
    </Card>
  );
}

function StartButton({ onDone, operationId }: { onDone: () => void; operationId: string }) {
  const translate = useTranslations("admin.bulk");
  const { run, pending } = useOperatorAction();
  return (
    <Button
      disabled={pending}
      onClick={async () => {
        const ok = await run(`/v1/panel/bulk/${operationId}/start`, { method: "POST" });
        if (ok) {
          onDone();
        }
      }}
      size="sm"
    >
      {translate("start")}
    </Button>
  );
}

function CancelButton({ onDone, operationId }: { onDone: () => void; operationId: string }) {
  const translate = useTranslations("admin.bulk");
  const { run, pending } = useOperatorAction();
  return (
    <Button
      disabled={pending}
      onClick={async () => {
        const ok = await run(`/v1/panel/bulk/${operationId}/cancel`, { method: "POST" });
        if (ok) {
          onDone();
        }
      }}
      size="sm"
      variant="destructive"
    >
      {translate("cancel")}
    </Button>
  );
}
