"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@omniflow/ui/tabs";
import Link from "next/link";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import {
  type AnomalySignal,
  type BlocklistMatch,
  type Listing,
  type Page,
  useOperatorAction,
} from "@/lib/operations";
import { useSession } from "@/lib/session";

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
          <TabsTrigger value="sources">{translate("tabs.sources")}</TabsTrigger>
        </TabsList>
        <TabsContent value="matches">
          <Matches active={tab === "matches"} />
        </TabsContent>
        <TabsContent value="anomalies">
          <Anomalies active={tab === "anomalies"} />
        </TabsContent>
        <TabsContent value="sources">
          <Sources active={tab === "sources"} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function Matches({ active }: { active: boolean }) {
  const translate = useTranslations("admin.risk");
  const locale = useLocale();
  const { can } = useSession();
  const [reason, setReason] = useState("");
  const { run, pending, error } = useOperatorAction();

  const key = active ? "/v1/panel/risk/matches?status=open" : null;
  const { data, isLoading, mutate } = useSWR<Page<BlocklistMatch>, ApiError>(key, fetcher);

  async function decide(matchID: string, decision: "allowed" | "blocked") {
    const ok = await run(`/v1/panel/risk/matches/${matchID}/decision`, {
      body: { decision },
      method: "POST",
      reason: reason.trim(),
    });
    if (ok) {
      setReason("");
      await mutate();
    }
  }

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <Card className="p-6">
        <StateNotice
          description={translate("empty.matchesDescription")}
          title={translate("empty.matches")}
          variant="empty"
        />
      </Card>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      {can("risk.write") && (
        <Card className="p-3">
          {/* One reason box for the queue: an operator reviewing ten matches in
              a row is recording one decision rationale, not ten. */}
          <Input
            onChange={(event) => setReason(event.target.value)}
            placeholder={translate("reasonPlaceholder")}
            value={reason}
          />
          {error && <p className="mt-2 text-danger-foreground text-xs">{error.message}</p>}
        </Card>
      )}

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
                  {new Date(match.detectedAt).toLocaleString(locale)}
                </TableCell>
                <TableCell className="font-mono text-[11px]">
                  <Link
                    className="underline-offset-2 hover:underline"
                    href={`/admin/customers/${match.customerId}`}
                  >
                    {match.customerId.slice(0, 8)}
                  </Link>
                </TableCell>
                <TableCell>{match.sourceName}</TableCell>
                <TableCell className="text-muted-foreground">{match.subjectKind}</TableCell>
                {can("risk.write") && (
                  <TableCell>
                    <div className="flex gap-2">
                      <Button
                        disabled={pending || reason.trim().length === 0}
                        onClick={() => decide(match.id, "allowed")}
                        size="sm"
                        variant="outline"
                      >
                        {translate("actions.allow")}
                      </Button>
                      <Button
                        disabled={pending || reason.trim().length === 0}
                        onClick={() => decide(match.id, "blocked")}
                        size="sm"
                        variant="destructive"
                      >
                        {translate("actions.block")}
                      </Button>
                    </div>
                  </TableCell>
                )}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>
    </div>
  );
}

function Anomalies({ active }: { active: boolean }) {
  const translate = useTranslations("admin.risk");
  const locale = useLocale();
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
      <Card className="p-6">
        <StateNotice
          description={translate("empty.anomaliesDescription")}
          title={translate("empty.anomalies")}
          variant="empty"
        />
      </Card>
    );
  }

  return (
    <div className="grid gap-3 lg:grid-cols-2">
      {items.map((signal) => (
        <Card className="flex flex-col gap-3 p-4" key={signal.id}>
          <div className="flex items-start justify-between gap-3">
            <div className="flex flex-col gap-0.5">
              <span className="font-medium">{translate(`metrics.${signal.metric}`)}</span>
              <span className="font-mono text-[11px] text-muted-foreground">
                {signal.subjectType}:{signal.subjectId.slice(0, 8)}
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
                <dt className="text-muted-foreground">{field}</dt>
                <dd className="truncate font-mono tabular-nums">{String(value)}</dd>
              </div>
            ))}
          </dl>

          <p className="text-muted-foreground text-xs">
            {new Date(signal.windowStart).toLocaleString(locale)} —{" "}
            {new Date(signal.windowEnd).toLocaleString(locale)}
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
  const translate = useTranslations("admin.risk");
  const locale = useLocale();
  const { data, isLoading } = useSWR<Listing<BlocklistSource>, ApiError>(
    active ? "/v1/panel/risk/sources" : null,
    fetcher,
  );

  if (isLoading) {
    return <Skeleton className="h-32 w-full" />;
  }
  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <Card className="p-6">
        <StateNotice
          description={translate("empty.sourcesDescription")}
          title={translate("empty.sources")}
          variant="empty"
        />
      </Card>
    );
  }

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
              <TableCell className="text-muted-foreground">{source.subjectKind}</TableCell>
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
                  {source.status}
                </Badge>
              </TableCell>
              <TableCell className="whitespace-nowrap font-mono text-[11px]" data-numeric>
                {source.lastRefreshAt ? new Date(source.lastRefreshAt).toLocaleString(locale) : "—"}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}
