"use client";

import { Card } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { formatDuration } from "@/lib/operations";

import type { SupportReportData } from "./types";

const WINDOWS = [7, 30, 90] as const;

/**
 * Workload and response time, with the definitions attached.
 *
 * The definitions ship with the numbers because a response-time report whose
 * definition is ambiguous is a report people argue about instead of acting on:
 * "average response time" means at least three different things depending on
 * who is asked, and none of them is what this measures.
 */
export function SupportReport({ active }: { active: boolean }) {
  const translate = useTranslations("admin.support.report");
  const [windowDays, setWindowDays] = useState(7);

  const { data, isLoading } = useSWR<SupportReportData, ApiError>(
    active ? `/v1/panel/support/report?windowDays=${windowDays}` : null,
    fetcher,
  );

  if (isLoading || !data) {
    return <Skeleton className="h-64 w-full" />;
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap gap-2">
        {WINDOWS.map((days) => (
          <button
            className={`rounded-md border px-3 py-1 text-sm ${
              windowDays === days ? "border-accent" : "border-border"
            }`}
            key={days}
            onClick={() => setWindowDays(days)}
            type="button"
          >
            {translate("window", { days })}
          </button>
        ))}
      </div>

      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-5">
        <Figure label={translate("open")} value={String(data.openTickets)} />
        <Figure label={translate("unassigned")} value={String(data.unassignedTickets)} />
        <Figure label={translate("breached")} value={String(data.breachedTickets)} />
        <Figure label={translate("resolved")} value={String(data.resolvedInWindow)} />
        <Figure
          label={translate("median")}
          value={
            data.medianFirstResponseSeconds > 0
              ? formatDuration(data.medianFirstResponseSeconds)
              : "—"
          }
        />
      </div>

      <Card className="overflow-x-auto">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{translate("operator")}</TableHead>
              <TableHead>{translate("replies")}</TableHead>
              <TableHead>{translate("openTickets")}</TableHead>
              <TableHead>{translate("resolvedTickets")}</TableHead>
              <TableHead>{translate("medianShort")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.operators.map((operator) => (
              <TableRow key={operator.operatorId}>
                <TableCell>{operator.displayName}</TableCell>
                <TableCell data-numeric>{operator.replies}</TableCell>
                <TableCell data-numeric>{operator.openTickets}</TableCell>
                <TableCell data-numeric>{operator.resolvedTickets}</TableCell>
                <TableCell data-numeric>
                  {operator.medianFirstResponseSeconds > 0
                    ? formatDuration(operator.medianFirstResponseSeconds)
                    : "—"}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>

      <Card className="flex flex-col gap-2 p-4">
        <h3 className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
          {translate("definitions")}
        </h3>
        <dl className="flex flex-col gap-1.5">
          {Object.entries(data.definitions).map(([key, definition]) => (
            <div className="flex flex-col" key={key}>
              <dt className="font-medium text-sm">{key}</dt>
              <dd className="text-muted-foreground text-sm">{definition}</dd>
            </div>
          ))}
        </dl>
      </Card>
    </div>
  );
}

function Figure({ label, value }: { label: string; value: string }) {
  return (
    <Card className="flex flex-col gap-0.5 p-3">
      <span className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
        {label}
      </span>
      <span className="font-semibold text-2xl tabular-nums">{value}</span>
    </Card>
  );
}
