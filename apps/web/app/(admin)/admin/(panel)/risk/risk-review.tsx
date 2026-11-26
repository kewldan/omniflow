"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@omniflow/ui/tabs";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR, { useSWRConfig } from "swr";

import { BanDialog } from "@/components/admin/ban-dialog";
import { CustomerChip } from "@/components/admin/customer-chip";
import { useEnumLabel } from "@/components/admin/enum-label";
import { ReasonDialog } from "@/components/admin/reason-dialog";
import { PageHeader } from "@/components/admin/resource-table";
import { StateNotice } from "@/components/admin/state-notice";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, fetcher } from "@/lib/api";
import {
  type AnomalySignal,
  type BlocklistMatch,
  type Listing,
  type Page,
  useOperatorAction,
} from "@/lib/operations";
import { useSession } from "@/lib/session";
import { CheckNow } from "./check-now";
import { NoSources, RuleEditor, SourceEditor } from "./rule-editor";
import { SharedDevices } from "./shared-devices";

type BlocklistSource = {
  id: string;
  slug: string;
  displayName: string;
  subjectKind: string;
  enabled: boolean;
  entryCount: number;
  status: string;
  lastErrorCode?: string;
  lastRefreshAt?: string;
};

/**
 * Risk review.
 *
 * Everything on this page is evidence somebody has to look at. Deciding a
 * blocklist match records a verdict and a reason; it does not suspend anybody.
 * Acknowledging or dismissing an anomaly changes nothing about the customer it
 * names. The adverse action, when there is one, is an ordinary customer
 * mutation on the customer's own page, with its own permission and its own
 * audit event — which is what makes it safe to run detection at all.
 */
export function RiskReview() {
  const translate = useTranslations("admin.risk");
  const [tab, setTab] = useState("matches");
  const { mutate } = useSWRConfig();

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />
      <Card className="border-warning/40 bg-warning/5 p-3 text-sm">
        {translate("noAutoAction")}
      </Card>

      <Tabs onValueChange={setTab} value={tab}>
        <TabsList>
          <TabsTrigger value="matches">{translate("tabs.matches")}</TabsTrigger>
          <TabsTrigger value="anomalies">{translate("tabs.anomalies")}</TabsTrigger>
          <TabsTrigger value="devices">{translate("tabs.devices")}</TabsTrigger>
          <TabsTrigger value="sources">{translate("tabs.sources")}</TabsTrigger>
          <TabsTrigger value="rules">{translate("tabs.rules")}</TabsTrigger>
        </TabsList>
        <TabsContent className="flex flex-col gap-3" value="matches">
          <CheckNow
            onChecked={() =>
              void mutate(
                (key) => typeof key === "string" && key.startsWith("/v1/panel/risk/matches"),
              )
            }
          />
          <Matches active={tab === "matches"} />
        </TabsContent>
        <TabsContent value="anomalies">
          <Anomalies active={tab === "anomalies"} />
        </TabsContent>
        <TabsContent value="devices">
          <SharedDevices active={tab === "devices"} />
        </TabsContent>
        <TabsContent value="sources">
          <Sources active={tab === "sources"} />
        </TabsContent>
        <TabsContent value="rules">
          <RuleEditor active={tab === "rules"} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function Matches({ active }: { active: boolean }) {
  const translate = useTranslations("admin.risk");
  const presets = useTranslations("admin.reasons.blocklistAllow");
  const banReasons = useTranslations("admin.moderation.ban.templates");
  const label = useEnumLabel();
  const { can } = useSession();
  const { run, pending, error } = useOperatorAction();
  const [allowing, setAllowing] = useState<BlocklistMatch | null>(null);
  const [blocking, setBlocking] = useState<BlocklistMatch | null>(null);

  const key = active ? "/v1/panel/risk/matches?status=open" : null;
  const { data, isLoading, mutate } = useSWR<Page<BlocklistMatch>, ApiError>(key, fetcher);

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <StateNotice
        description={translate("empty.matchesDescription")}
        title={translate("empty.matches")}
        variant="empty"
      />
    );
  }

  return (
    <div className="flex flex-col gap-3">
      <Card className="overflow-x-auto">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("columns.detectedAt")}</TableHead>
              <TableHead>{translate("columns.customer")}</TableHead>
              <TableHead>{translate("columns.source")}</TableHead>
              <TableHead>{translate("columns.subject")}</TableHead>
              {can("risk.write") && <TableHead>{translate("columns.decision")}</TableHead>}
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((match) => (
              <TableRow key={match.id}>
                <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
                  <Timestamp value={match.detectedAt} />
                </TableCell>
                <TableCell>
                  <CustomerChip id={match.customerId} />
                </TableCell>
                <TableCell>{match.sourceName}</TableCell>
                <TableCell className="text-muted-foreground">
                  {label("subjectKind", match.subjectKind)}
                </TableCell>
                {can("risk.write") && (
                  <TableCell>
                    <div className="flex gap-2">
                      <Button onClick={() => setAllowing(match)} size="sm" variant="outline">
                        {translate("actions.allow")}
                      </Button>
                      {can("customers.write") && (
                        <Button onClick={() => setBlocking(match)} size="sm" variant="destructive">
                          {translate("actions.block")}
                        </Button>
                      )}
                    </div>
                  </TableCell>
                )}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>

      <ReasonDialog
        confirmLabel={translate("actions.allow")}
        description={translate("allowDescription")}
        error={error?.message}
        onConfirm={async (reason) => {
          if (
            allowing &&
            (await run(`/v1/panel/risk/matches/${allowing.id}/decision`, {
              body: { decision: "allowed" },
              method: "POST",
              reason,
            }))
          ) {
            setAllowing(null);
            await mutate();
          }
        }}
        onOpenChange={(open) => !open && setAllowing(null)}
        open={allowing !== null}
        pending={pending}
        presets={["family", "falsePositive", "checked", "resolved"].map((key) => presets(key))}
        title={translate("allowTitle")}
      />
      <BanDialog
        defaultReason="rules"
        description={translate("blockDescription")}
        error={error?.message}
        key={blocking?.id}
        onOpenChange={(open) => !open && setBlocking(null)}
        onSubmit={async (ban) => {
          if (
            blocking &&
            (await run(`/v1/panel/risk/matches/${blocking.id}/decision`, {
              body: { ban, decision: "blocked" },
              method: "POST",
              reason: `${blocking.sourceName}: ${banReasons(ban.reason)}`,
            }))
          ) {
            setBlocking(null);
            await mutate();
          }
        }}
        open={blocking !== null}
        pending={pending}
        title={translate("blockTitle")}
      />
    </div>
  );
}

function Anomalies({ active }: { active: boolean }) {
  const translate = useTranslations("admin.risk");
  const label = useEnumLabel();
  const { can } = useSession();
  const { run, pending } = useOperatorAction();

  const key = active ? "/v1/panel/risk/anomalies?status=open" : null;
  const { data, isLoading, mutate } = useSWR<Page<AnomalySignal>, ApiError>(key, fetcher);

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <StateNotice
        description={translate("empty.anomaliesDescription")}
        title={translate("empty.anomalies")}
        variant="empty"
      />
    );
  }

  return (
    <div className="grid gap-3 lg:grid-cols-2">
      {items.map((signal) => (
        <Card className="flex flex-col gap-3 p-4" key={signal.id}>
          <div className="flex items-start justify-between gap-3">
            <div className="flex flex-col gap-0.5">
              <span className="font-medium">{translate(`metrics.${signal.metric}`)}</span>
              <span className="text-[11px] text-muted-foreground">
                {label("anomalySubject", signal.subjectType)}{" "}
                <span className="font-mono">{signal.subjectId.slice(0, 8)}</span>
              </span>
            </div>
            <Badge variant={signal.severity === "alert" ? "danger" : "warning"}>
              {translate(`severity.${signal.severity}`)}
            </Badge>
          </div>

          <p className="text-sm">
            {translate("observed", { observed: signal.observed, threshold: signal.threshold })}
          </p>

          {/* The evidence is the arithmetic that produced the signal, and only
              that: no message body, no link, no customer content. */}
          <dl className="grid grid-cols-2 gap-1 text-xs">
            {Object.entries(signal.evidence ?? {}).map(([field, value]) => (
              <div className="flex justify-between gap-2" key={field}>
                <dt className="text-muted-foreground">{label("anomalyEvidence", field)}</dt>
                <dd className="truncate font-mono tabular-nums">{String(value)}</dd>
              </div>
            ))}
          </dl>

          <p className="text-muted-foreground text-xs">
            <Timestamp value={signal.windowStart} /> — <Timestamp value={signal.windowEnd} />
          </p>

          {can("risk.write") && (
            <div className="flex gap-2">
              <Button
                disabled={pending}
                onClick={async () => {
                  if (
                    await run(`/v1/panel/risk/anomalies/${signal.id}/review`, {
                      body: { status: "acknowledged" },
                      method: "POST",
                    })
                  ) {
                    await mutate();
                  }
                }}
                size="sm"
                variant="outline"
              >
                {translate("actions.acknowledge")}
              </Button>
              <Button
                disabled={pending}
                onClick={async () => {
                  if (
                    await run(`/v1/panel/risk/anomalies/${signal.id}/review`, {
                      body: { status: "dismissed" },
                      method: "POST",
                    })
                  ) {
                    await mutate();
                  }
                }}
                size="sm"
                variant="ghost"
              >
                {translate("actions.dismiss")}
              </Button>
            </div>
          )}
        </Card>
      ))}
    </div>
  );
}

function Sources({ active }: { active: boolean }) {
  const locale = useLocale();
  const { can } = useSession();
  const { data, isLoading, mutate } = useSWR<Listing<BlocklistSource>, ApiError>(
    active ? "/v1/panel/risk/sources" : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];

  return (
    <div className="flex flex-col gap-3">
      {can("risk.write") && (
        <SourceEditor existingSlugs={items.map((source) => source.slug)} onSaved={() => mutate()} />
      )}
      {items.length === 0 ? (
        <Card className="p-6">
          <NoSources />
        </Card>
      ) : (
        <SourceTable items={items} locale={locale} />
      )}
    </div>
  );
}

function SourceTable({ items, locale }: { items: BlocklistSource[]; locale: string }) {
  const translate = useTranslations("admin.risk");
  const label = useEnumLabel();
  return (
    <Card className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{translate("columns.sourceName")}</TableHead>
            <TableHead>{translate("columns.subject")}</TableHead>
            <TableHead>{translate("columns.entries")}</TableHead>
            <TableHead>{translate("columns.health")}</TableHead>
            <TableHead>{translate("columns.lastRefresh")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((source) => (
            <TableRow key={source.id}>
              <TableCell>
                <span className="flex items-center gap-2">
                  {source.displayName}
                  {!source.enabled && <Badge variant="neutral">{translate("disabled")}</Badge>}
                </span>
              </TableCell>
              <TableCell className="text-muted-foreground">
                {label("subjectKind", source.subjectKind)}
              </TableCell>
              <TableCell data-numeric>{source.entryCount.toLocaleString(locale)}</TableCell>
              <TableCell>
                <Badge
                  variant={
                    source.status === "healthy"
                      ? "success"
                      : source.status === "failing"
                        ? "danger"
                        : "neutral"
                  }
                >
                  {label("health", source.status)}
                </Badge>
              </TableCell>
              <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
                <Timestamp value={source.lastRefreshAt} />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}
