"use client";

import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import dynamic from "next/dynamic";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { formatDuration } from "@/lib/operations";

import type { SupportReportData } from "./types";

/**
 * The chart arrives after the report does.
 *
 * The charting library is a quarter of a megabyte and loading it with this page
 * put it over the performance budget. The five figures above render from the
 * same request without it, so it is fetched when this section renders rather
 * than when the page does. `ssr: false` because the chart measures its own
 * container, which has no size on the server.
 */
const OperatorWorkload = dynamic(
  () => import("./operator-workload").then((module) => module.OperatorWorkload),
  { loading: () => <Skeleton className="h-40 w-full" />, ssr: false },
);

const WINDOWS = [7, 30, 90] as const;

/** The five headline figures, in the order an operator reads them. */
const FIGURES = [
  "openTickets",
  "unassignedTickets",
  "breachedTickets",
  "resolvedInWindow",
  "medianFirstResponseSeconds",
] as const;

/**
 * Workload and response time.
 *
 * Every figure carries its own definition, directly under it. They used to be a
 * list at the foot of the page under the heading "What these numbers mean" —
 * six paragraphs, in English regardless of the operator's language, at the
 * furthest point from the numbers they explained. A definition nobody reads is
 * the same as no definition, and this report needs them: "average response
 * time" means at least three different things depending on who is asked, and
 * none of them is what this measures.
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

  const values: Record<(typeof FIGURES)[number], string> = {
    breachedTickets: String(data.breachedTickets),
    medianFirstResponseSeconds:
      data.medianFirstResponseSeconds > 0 ? formatDuration(data.medianFirstResponseSeconds) : "—",
    openTickets: String(data.openTickets),
    resolvedInWindow: String(data.resolvedInWindow),
    unassignedTickets: String(data.unassignedTickets),
  };

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap gap-2">
        {WINDOWS.map((days) => (
          <Button
            key={days}
            onClick={() => setWindowDays(days)}
            size="sm"
            type="button"
            variant={windowDays === days ? "default" : "outline"}
          >
            {translate("window", { days })}
          </Button>
        ))}
      </div>

      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-5">
        {FIGURES.map((key) => (
          <Card className="flex flex-col gap-1 p-3" key={key}>
            <span className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
              {translate(`figures.${key}.label`)}
            </span>
            <span className="font-semibold text-2xl tabular-nums">{values[key]}</span>
            {/* The definition sits under the number rather than behind a
                tooltip: it is the part somebody reads once and then trusts, and
                a tooltip is exactly the affordance nobody discovers. */}
            <span className="text-muted-foreground text-xs">
              {translate(`figures.${key}.definition`)}
            </span>
          </Card>
        ))}
      </div>

      <Card className="p-4">
        <OperatorWorkload operators={data.operators} />
      </Card>
    </div>
  );
}
