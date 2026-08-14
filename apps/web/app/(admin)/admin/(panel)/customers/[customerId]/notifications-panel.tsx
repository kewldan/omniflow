"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { toast } from "@omniflow/ui/toast";
import { Send } from "lucide-react";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import type { Delivery, DeliveryPage, DeliverySummary, QueuedTest } from "@/lib/operations";
import { useSession } from "@/lib/session";

/**
 * What this customer was actually sent.
 *
 * The preferences a customer sets say what they are meant to receive. That is a
 * setting, not evidence, and it leaves "I never got the expiry warning" as a
 * claim nobody in the conversation can check.
 *
 * The status column is the answer, and `suppressed` is the most useful value on
 * it: the message was never sent, on purpose, for a stated reason. An operator
 * reading `quiet_hours` or `no_consent` can say what happened and what to change
 * about it. Reading nothing at all leaves them guessing about whether the bot
 * works.
 */
export function NotificationsPanel({
  active,
  base,
  customerId,
}: {
  active: boolean;
  base: string;
  customerId: string;
}) {
  const translate = useTranslations("admin.notifications");
  const locale = useLocale();
  const { can } = useSession();

  const [status, setStatus] = useState("all");
  const [sending, setSending] = useState(false);

  const query = status === "all" ? "" : `?status=${encodeURIComponent(status)}`;
  const { data, error, isLoading, mutate } = useSWR<DeliveryPage, ApiError>(
    active ? `${base}/notifications${query}` : null,
    fetcher,
  );
  const summary = useSWR<{ summaries: DeliverySummary[] | null }, ApiError>(
    active ? `${base}/notifications/summary` : null,
    fetcher,
  );

  // The response body is read rather than discarded, which is why this calls
  // the transport directly instead of the usual mutation hook: `queued: false`
  // means an identical test was already waiting, and telling the operator that
  // is the difference between "nothing happened" and "it is already on its way".
  async function sendTest() {
    setSending(true);
    try {
      const queued = await apiFetch<QueuedTest>(
        `/v1/panel/customers/${customerId}/notifications/test`,
        { method: "POST" },
      );
      // Queued, not sent. Saying "sent" here would be the exact failure this
      // screen exists to expose: the notifier collects on its own schedule, and
      // the outcome appears in the table below when it has.
      toast.success(queued.queued ? translate("queued") : translate("alreadyQueued"));
      mutate();
      summary.mutate();
    } catch {
      toast.error(translate("testFailed"));
    } finally {
      setSending(false);
    }
  }

  if (isLoading) {
    return <Skeleton className="h-64 w-full" />;
  }
  if (error) {
    return <StateNotice title={translate("failed")} variant="danger" />;
  }

  const deliveries = data?.deliveries ?? [];
  const summaries = summary.data?.summaries ?? [];

  return (
    <div className="flex flex-col gap-4">
      <Card className="flex flex-wrap items-end justify-between gap-4 p-4">
        <div className="flex flex-wrap gap-x-6 gap-y-3">
          {summaries.length === 0 ? (
            <p className="text-subtle-foreground text-sm">{translate("neverSent")}</p>
          ) : (
            summaries.map((row) => (
              <span className="flex flex-col" key={row.kind}>
                <span className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
                  {label(translate, "kind", row.kind)}
                </span>
                <span className="text-sm">
                  {translate("counts", {
                    sent: row.sent,
                    total: row.total,
                  })}
                  {row.suppressed > 0 && (
                    <span className="ml-2 text-subtle-foreground">
                      {translate("heldBack", { count: row.suppressed })}
                    </span>
                  )}
                </span>
              </span>
            ))
          )}
        </div>

        {can("support.write") && (
          <Button disabled={sending} onClick={sendTest} variant="secondary">
            <Send aria-hidden className="size-4" />
            {translate("sendTest")}
          </Button>
        )}
      </Card>

      <div className="flex items-center gap-3">
        <Select onValueChange={setStatus} value={status}>
          <SelectTrigger className="w-56">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{translate("status.all")}</SelectItem>
            <SelectItem value="sent">{translate("status.sent")}</SelectItem>
            <SelectItem value="failed">{translate("status.failed")}</SelectItem>
            <SelectItem value="suppressed">{translate("status.suppressed")}</SelectItem>
            <SelectItem value="deferred">{translate("status.deferred")}</SelectItem>
            <SelectItem value="pending">{translate("status.pending")}</SelectItem>
          </SelectContent>
        </Select>
        <p className="text-subtle-foreground text-sm">
          {translate("total", { count: data?.total ?? 0 })}
        </p>
      </div>

      {deliveries.length === 0 ? (
        <StateNotice title={translate("empty")} variant={status === "all" ? "empty" : "filtered"} />
      ) : (
        <Card className="overflow-x-auto p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{translate("column.kind")}</TableHead>
                <TableHead>{translate("column.status")}</TableHead>
                <TableHead>{translate("column.reason")}</TableHead>
                <TableHead>{translate("column.when")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {deliveries.map((delivery) => (
                <DeliveryRow delivery={delivery} key={delivery.id} locale={locale} />
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </div>
  );
}

function DeliveryRow({ delivery, locale }: { delivery: Delivery; locale: string }) {
  const translate = useTranslations("admin.notifications");
  const when = delivery.sentAt ?? delivery.deferredUntil ?? delivery.scheduledAt;

  return (
    <TableRow>
      <TableCell>
        <span className="flex flex-col">
          <span>{label(translate, "kind", delivery.kind)}</span>
          {delivery.subscriptionSlot ? (
            <span className="text-subtle-foreground text-xs">
              {delivery.subscriptionLabel || translate("slot", { slot: delivery.subscriptionSlot })}
            </span>
          ) : null}
        </span>
      </TableCell>
      <TableCell>
        <Badge variant={statusTone(delivery.status)}>
          {label(translate, "status", delivery.status)}
        </Badge>
      </TableCell>
      <TableCell className="text-subtle-foreground text-sm">
        {delivery.reason ? label(translate, "reason", delivery.reason) : "—"}
        {delivery.failureCount > 0 && (
          <span className="ml-2">{translate("attempts", { count: delivery.failureCount })}</span>
        )}
      </TableCell>
      <TableCell className="whitespace-nowrap text-sm">
        {new Date(when).toLocaleString(locale)}
      </TableCell>
    </TableRow>
  );
}

/**
 * Names a kind, a status, or a suppression reason.
 *
 * The untranslated code is shown when there is no phrase for it. A kind added to
 * the schema before the copy catches up is a legible `announcement` rather than
 * a missing-translation crash on a screen somebody opened to answer a question.
 */
function label(
  translate: ReturnType<typeof useTranslations<"admin.notifications">>,
  group: string,
  value: string,
): string {
  const key = `${group}.${value}`;
  // biome-ignore lint/suspicious/noExplicitAny: the key is a runtime code, not a literal in the message catalogue.
  return translate.has(key as any) ? translate(key as any) : value;
}

/**
 * `suppressed` is neutral rather than a warning on purpose. It is the
 * installation doing what it was told — honouring quiet hours, a frequency cap,
 * or an opt-out — and colouring it as a fault would send operators looking for a
 * problem that is a working feature.
 */
function statusTone(status: string): "success" | "danger" | "warning" | "neutral" {
  switch (status) {
    case "sent":
      return "success";
    case "failed":
      return "danger";
    case "deferred":
    case "pending":
      return "warning";
    default:
      return "neutral";
  }
}
