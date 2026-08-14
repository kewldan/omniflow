"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@omniflow/ui/tabs";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import {
  type FulfillmentOperation,
  formatDuration,
  type Listing,
  type Page,
  useOperatorAction,
  type WebhookEvent,
} from "@/lib/operations";
import { useSession } from "@/lib/session";

import { HealthPanel } from "./health-panel";

type OutboxEntry = { id: string; topic: string; occurredAt: string; ageSeconds: number };

type Drift = {
  id: string;
  entitlementId: string;
  customerId: string;
  kind: string;
  detectedAt: string;
};

/**
 * Fulfillment, webhook, outbox, and drift diagnostics.
 *
 * None of these views shows a provider payload or a Remnawave response body.
 * They carry a status, a classification, and a correlation identifier, which is
 * what an operator needs to decide whether to retry — and none of which can
 * leak a subscription link into an operator's screen.
 */
export function SystemDiagnostics() {
  const translate = useTranslations("admin.system");
  const [tab, setTab] = useState("health");

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />
      <Tabs onValueChange={setTab} value={tab}>
        <TabsList>
          <TabsTrigger value="health">{translate("tabs.health")}</TabsTrigger>
          <TabsTrigger value="jobs">{translate("tabs.jobs")}</TabsTrigger>
          <TabsTrigger value="webhooks">{translate("tabs.webhooks")}</TabsTrigger>
          <TabsTrigger value="drift">{translate("tabs.drift")}</TabsTrigger>
          <TabsTrigger value="outbox">{translate("tabs.outbox")}</TabsTrigger>
        </TabsList>
        <TabsContent value="health">
          <HealthPanel active={tab === "health"} />
        </TabsContent>
        <TabsContent value="jobs">
          <Jobs active={tab === "jobs"} />
        </TabsContent>
        <TabsContent value="webhooks">
          <Webhooks active={tab === "webhooks"} />
        </TabsContent>
        <TabsContent value="drift">
          <Drifts active={tab === "drift"} />
        </TabsContent>
        <TabsContent value="outbox">
          <Outbox active={tab === "outbox"} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

/**
 * The fulfillment queue, filtered to failures.
 *
 * A retry re-queues the same operation under the same idempotency key, so the
 * worker still performs it exactly once. Cancelling is only offered for an
 * operation that has not succeeded: a completed provisioning cannot be
 * retracted by a panel click.
 */
function Jobs({ active }: { active: boolean }) {
  const translate = useTranslations("admin.system");
  const locale = useLocale();
  const { can } = useSession();
  const { run, pending } = useOperatorAction();

  const { data, isLoading, mutate } = useSWR<Page<FulfillmentOperation>, ApiError>(
    active ? "/v1/panel/system/jobs?status=failed" : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <StateNotice
        description={translate("empty.jobsDescription")}
        title={translate("empty.jobs")}
        variant="empty"
      />
    );
  }

  return (
    <Card className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{translate("columns.createdAt")}</TableHead>
            <TableHead>{translate("columns.operation")}</TableHead>
            <TableHead>{translate("columns.attempts")}</TableHead>
            <TableHead>{translate("columns.errorCode")}</TableHead>
            <TableHead>{translate("columns.correlation")}</TableHead>
            {can("system.write") && <TableHead>{translate("columns.actions")}</TableHead>}
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((operation) => (
            <TableRow key={operation.id}>
              <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
                {new Date(operation.createdAt).toLocaleString(locale)}
              </TableCell>
              <TableCell>{operation.operation}</TableCell>
              <TableCell data-numeric>{operation.attemptCount}</TableCell>
              <TableCell className="font-mono text-[11px] text-muted-foreground">
                {operation.lastErrorCode ?? "—"}
              </TableCell>
              <TableCell className="max-w-40 truncate font-mono text-[11px] text-muted-foreground">
                {operation.correlationId}
              </TableCell>
              {can("system.write") && (
                <TableCell>
                  <div className="flex gap-2">
                    <Button
                      disabled={pending}
                      onClick={async () => {
                        if (
                          await run(`/v1/panel/system/jobs/${operation.id}/retry`, {
                            method: "POST",
                          })
                        ) {
                          await mutate();
                        }
                      }}
                      size="sm"
                      variant="outline"
                    >
                      {translate("actions.retry")}
                    </Button>
                    <Button
                      disabled={pending}
                      onClick={async () => {
                        if (
                          await run(`/v1/panel/system/jobs/${operation.id}/cancel`, {
                            method: "POST",
                          })
                        ) {
                          await mutate();
                        }
                      }}
                      size="sm"
                      variant="ghost"
                    >
                      {translate("actions.cancel")}
                    </Button>
                  </div>
                </TableCell>
              )}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

function Webhooks({ active }: { active: boolean }) {
  const translate = useTranslations("admin.system");
  const locale = useLocale();
  const { can } = useSession();
  const { run, pending } = useOperatorAction();

  const { data, isLoading, mutate } = useSWR<Page<WebhookEvent>, ApiError>(
    active ? "/v1/panel/system/webhooks" : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return <StateNotice title={translate("empty.webhooks")} variant="empty" />;
  }

  return (
    <Card className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{translate("columns.receivedAt")}</TableHead>
            <TableHead>{translate("columns.provider")}</TableHead>
            <TableHead>{translate("columns.signature")}</TableHead>
            <TableHead>{translate("columns.status")}</TableHead>
            {can("system.write") && <TableHead>{translate("columns.actions")}</TableHead>}
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((event) => (
            <TableRow key={event.id}>
              <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
                {new Date(event.receivedAt).toLocaleString(locale)}
              </TableCell>
              <TableCell>{event.provider}</TableCell>
              <TableCell>
                <Badge variant={event.signatureValid ? "success" : "danger"}>
                  {translate(event.signatureValid ? "signature.valid" : "signature.invalid")}
                </Badge>
              </TableCell>
              <TableCell>
                <Badge variant={event.status === "failed" ? "danger" : "neutral"}>
                  {event.status}
                </Badge>
              </TableCell>
              {can("system.write") && (
                <TableCell>
                  {/* Only a failed or ignored event may be replayed. Reprocessing
                      is keyed on the provider event identifier, so a second pass
                      reaches the same terminal state instead of applying twice. */}
                  <Button
                    disabled={pending || !(event.status === "failed" || event.status === "ignored")}
                    onClick={async () => {
                      if (
                        await run(`/v1/panel/system/webhooks/${event.id}/replay`, {
                          method: "POST",
                        })
                      ) {
                        await mutate();
                      }
                    }}
                    size="sm"
                    variant="outline"
                  >
                    {translate("actions.replay")}
                  </Button>
                </TableCell>
              )}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

function Drifts({ active }: { active: boolean }) {
  const translate = useTranslations("admin.system");
  const locale = useLocale();
  const { data, isLoading } = useSWR<Listing<Drift>, ApiError>(
    active ? "/v1/panel/system/drift" : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <StateNotice
        description={translate("empty.driftDescription")}
        title={translate("empty.drift")}
        variant="empty"
      />
    );
  }

  return (
    <Card className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{translate("columns.detectedAt")}</TableHead>
            <TableHead>{translate("columns.kind")}</TableHead>
            <TableHead>{translate("columns.customer")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((drift) => (
            <TableRow key={drift.id}>
              <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
                {new Date(drift.detectedAt).toLocaleString(locale)}
              </TableCell>
              <TableCell>{translate(`driftKind.${drift.kind}`)}</TableCell>
              <TableCell className="font-mono text-[11px] text-muted-foreground">
                {drift.customerId.slice(0, 8)}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}

function Outbox({ active }: { active: boolean }) {
  const translate = useTranslations("admin.system");
  const { data, isLoading } = useSWR<Listing<OutboxEntry>, ApiError>(
    active ? "/v1/panel/system/outbox" : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <StateNotice
        description={translate("empty.outboxDescription")}
        title={translate("empty.outbox")}
        variant="empty"
      />
    );
  }

  return (
    <Card className="overflow-x-auto">
      {/* Topic and age only: an outbox payload is a domain event that can name a
          customer and an amount, and "is the publisher running" needs neither. */}
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{translate("columns.topic")}</TableHead>
            <TableHead>{translate("columns.age")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((entry) => (
            <TableRow key={entry.id}>
              <TableCell className="font-mono text-[12px]">{entry.topic}</TableCell>
              <TableCell data-numeric>{formatDuration(entry.ageSeconds)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}
