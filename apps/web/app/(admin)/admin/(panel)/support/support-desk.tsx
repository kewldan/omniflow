"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@omniflow/ui/tabs";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { PageHeader } from "@/components/admin/resource-table";
import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher, toQuery } from "@/lib/api";
import { formatDuration, type Listing, type Page } from "@/lib/operations";
import { useUrlFilters } from "@/lib/use-url-filters";

import { SupportReport } from "./support-report";
import { TicketConversation } from "./ticket-conversation";
import type { SupportQueue, SupportTicket } from "./types";

const STATUSES = ["", "open", "pending", "resolved", "closed"] as const;

/**
 * The support desk.
 *
 * The queue is the primary view because that is the question an operator
 * actually arrives with: what is waiting, and who has it. A ticket opens beside
 * the queue rather than replacing it, so working through a backlog does not mean
 * navigating back after every reply.
 */
export function SupportDesk() {
  const translate = useTranslations("admin.support");
  const [tab, setTab] = useState("queue");

  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />
      <Tabs onValueChange={setTab} value={tab}>
        <TabsList>
          <TabsTrigger value="queue">{translate("tabs.queue")}</TabsTrigger>
          <TabsTrigger value="report">{translate("tabs.report")}</TabsTrigger>
        </TabsList>
        <TabsContent value="queue">
          <Queue active={tab === "queue"} />
        </TabsContent>
        <TabsContent value="report">
          <SupportReport active={tab === "report"} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function Queue({ active }: { active: boolean }) {
  const translate = useTranslations("admin.support");
  const locale = useLocale();
  const { filters, setFilter, reset } = useUrlFilters(["queueId", "status", "ticketId"]);

  const { data: queues } = useSWR<Listing<SupportQueue>, ApiError>(
    active ? "/v1/panel/support/queues" : null,
    fetcher,
    { refreshInterval: 30_000 },
  );
  const query = toQuery({
    pageSize: 50,
    queueId: filters.queueId,
    status: filters.status || "open",
  });
  const {
    data,
    isLoading,
    mutate: refresh,
  } = useSWR<Page<SupportTicket>, ApiError>(
    active ? `/v1/panel/support/tickets${query}` : null,
    fetcher,
    { refreshInterval: 15_000 },
  );

  const tickets = data?.items ?? [];

  return (
    <div className="flex flex-col gap-4">
      {/* The three counts per queue are what an operator reads before choosing
          what to work on: how much is here, how much nobody owns, and how much
          is already past what the queue promised. */}
      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
        {(queues?.items ?? []).map((queue) => (
          <Card
            className={`cursor-pointer p-3 ${filters.queueId === queue.id ? "border-accent" : ""}`}
            key={queue.id}
            onClick={() => setFilter("queueId", filters.queueId === queue.id ? "" : queue.id)}
          >
            <div className="flex items-baseline justify-between gap-2">
              <span className="font-medium text-sm">{queue.nameEn}</span>
              <span className="font-semibold text-lg tabular-nums">{queue.openCount}</span>
            </div>
            <div className="mt-1 flex flex-wrap gap-2 text-xs">
              {queue.unassignedCount > 0 && (
                <Badge variant="warning">
                  {translate("unassigned", { count: queue.unassignedCount })}
                </Badge>
              )}
              {queue.breachedCount > 0 && (
                <Badge variant="danger">
                  {translate("breached", { count: queue.breachedCount })}
                </Badge>
              )}
              {queue.firstResponseTargetSeconds > 0 && (
                <span className="text-muted-foreground">
                  {translate("target", {
                    duration: formatDuration(queue.firstResponseTargetSeconds),
                  })}
                </span>
              )}
            </div>
          </Card>
        ))}
      </div>

      <div className="flex flex-wrap items-center gap-2">
        {STATUSES.map((status) => (
          <Button
            key={status || "all"}
            onClick={() => setFilter("status", status)}
            size="sm"
            variant={(filters.status || "open") === (status || "open") ? "default" : "outline"}
          >
            {translate(`status.${status || "all"}`)}
          </Button>
        ))}
        <Button className="ml-auto" onClick={reset} size="sm" variant="ghost">
          {translate("resetFilters")}
        </Button>
      </div>

      <div className="grid gap-4 lg:grid-cols-[minmax(0,22rem)_1fr]">
        <div className="flex max-h-[42rem] flex-col gap-2 overflow-y-auto">
          {isLoading ? (
            <Skeleton className="h-64 w-full" />
          ) : tickets.length === 0 ? (
            <StateNotice
              description={translate("empty.description")}
              title={translate("empty.title")}
              variant="empty"
            />
          ) : (
            tickets.map((ticket) => (
              <Card
                className={`cursor-pointer p-3 ${
                  filters.ticketId === ticket.id ? "border-accent" : ""
                }`}
                key={ticket.id}
                onClick={() => setFilter("ticketId", ticket.id)}
              >
                <div className="flex items-baseline justify-between gap-2">
                  <span className="truncate font-medium text-sm">
                    {ticket.subject || translate("noSubject")}
                  </span>
                  {ticket.unreadCount > 0 && <Badge variant="warning">{ticket.unreadCount}</Badge>}
                </div>
                <div className="mt-1 flex flex-wrap items-center gap-2 text-xs">
                  <Badge variant={priorityVariant(ticket.priority)}>
                    {translate(`priority.${ticket.priority}`)}
                  </Badge>
                  {ticket.firstResponseBreached && (
                    <Badge variant="danger">{translate("overdue")}</Badge>
                  )}
                  <span className="text-muted-foreground">
                    {ticket.assigneeName || translate("nobody")}
                  </span>
                  <span className="ml-auto font-mono text-[11px] text-muted-foreground">
                    {new Date(ticket.lastMessageAt).toLocaleDateString(locale)}
                  </span>
                </div>
                {ticket.tags.length > 0 && (
                  <div className="mt-1 flex flex-wrap gap-1">
                    {ticket.tags.map((tag) => (
                      <span
                        className="rounded border border-border px-1.5 py-0.5 text-[10px]"
                        key={tag}
                      >
                        {tag}
                      </span>
                    ))}
                  </div>
                )}
              </Card>
            ))
          )}
        </div>

        {filters.ticketId ? (
          <TicketConversation
            onChanged={() => refresh()}
            queues={queues?.items ?? []}
            ticketId={filters.ticketId}
          />
        ) : tickets.length > 0 ? (
          <StateNotice title={translate("selectTicket")} variant="empty" />
        ) : // Nothing to select, so nothing is offered. Asking an operator to
        // pick a ticket beside a list that says there are none is two empty
        // states side by side, each contradicting the other's suggestion.
        null}
      </div>
    </div>
  );
}

function priorityVariant(priority: string): "danger" | "warning" | "neutral" {
  if (priority === "urgent") {
    return "danger";
  }
  if (priority === "high") {
    return "warning";
  }
  return "neutral";
}
