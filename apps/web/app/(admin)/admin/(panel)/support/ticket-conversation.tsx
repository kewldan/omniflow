"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Skeleton } from "@omniflow/ui/skeleton";
import Link from "next/link";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { StateNotice } from "@/components/admin/state-notice";
import { type ApiError, fetcher } from "@/lib/api";
import { useSubmission } from "@/lib/idempotency";
import { type Listing, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";

import type { CannedResponse, SupportQueue, SupportTag, TicketDetail } from "./types";

const PRIORITIES = ["low", "normal", "high", "urgent"] as const;
const STATUSES = ["open", "pending", "resolved", "closed"] as const;

/**
 * One conversation, with everything needed to answer it.
 *
 * Customer messages and internal notes are rendered in one timeline but come
 * from two lists and are visually distinct, because the cost of confusing them
 * is an operator's private note reaching the customer it is about.
 */
export function TicketConversation({
  onChanged,
  queues,
  ticketId,
}: {
  onChanged: () => void;
  queues: SupportQueue[];
  ticketId: string;
}) {
  const translate = useTranslations("admin.support");
  const locale = useLocale();
  const { can } = useSession();

  const { data, error, isLoading, mutate } = useSWR<TicketDetail, ApiError>(
    `/v1/panel/support/tickets/${ticketId}`,
    fetcher,
    { refreshInterval: 15_000 },
  );
  const { data: tags } = useSWR<Listing<SupportTag>, ApiError>("/v1/panel/support/tags", fetcher);

  if (error) {
    return <StateNotice title={translate("loadFailed")} variant="danger" />;
  }
  if (isLoading || !data) {
    return <Skeleton className="h-96 w-full" />;
  }

  const { ticket } = data;
  const writable = can("support.write") && ticket.status !== "merged";

  // The two lists are merged for display only, and each entry keeps the kind it
  // came from so the render can never mistake one for the other.
  const timeline = [
    ...data.messages.map((message) => ({ kind: "message" as const, value: message })),
    ...data.notes.map((note) => ({ kind: "note" as const, value: note })),
  ].sort((left, right) => left.value.createdAt.localeCompare(right.value.createdAt));

  return (
    <Card className="flex max-h-[42rem] flex-col gap-3 p-4">
      <header className="flex flex-col gap-2 border-border border-b pb-3">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <h2 className="font-semibold">{ticket.subject || translate("noSubject")}</h2>
          <Link
            className="font-mono text-[11px] underline-offset-2 hover:underline"
            href={`/admin/customers/${ticket.customerId}`}
          >
            {ticket.customerId.slice(0, 8)}
          </Link>
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <Badge variant="neutral">{translate(`status.${ticket.status}`)}</Badge>
          <Badge variant="neutral">{translate(`priority.${ticket.priority}`)}</Badge>
          <span className="text-muted-foreground">{ticket.queueCode}</span>
          <span className="text-muted-foreground">
            {ticket.assigneeName || translate("nobody")}
          </span>
          {ticket.reopenedCount > 0 && (
            // Reopens are the signal that an answer did not actually answer the
            // question, which is worth showing beside the ticket rather than
            // only in a report.
            <Badge variant="warning">
              {translate("reopened", { count: ticket.reopenedCount })}
            </Badge>
          )}
          {ticket.mergedIntoTicketId && <Badge variant="neutral">{translate("mergedAway")}</Badge>}
        </div>
        {writable && (
          <TicketControls
            onChanged={() => {
              void mutate();
              onChanged();
            }}
            queues={queues}
            tags={tags?.items ?? []}
            ticket={ticket}
          />
        )}
      </header>

      <div className="flex flex-1 flex-col gap-3 overflow-y-auto">
        {timeline.map((entry) =>
          entry.kind === "note" ? (
            <div
              className="rounded-md border border-warning-border border-dashed bg-warning-surface p-2"
              key={`note-${entry.value.id}`}
            >
              <div className="flex items-baseline justify-between gap-2">
                <span className="font-mono text-[10px] uppercase tracking-[0.12em]">
                  {translate("internalNote")}
                </span>
                <span className="font-mono text-[11px] text-muted-foreground">
                  {new Date(entry.value.createdAt).toLocaleString(locale)}
                </span>
              </div>
              <p className="whitespace-pre-wrap text-sm">{entry.value.body}</p>
              <span className="text-muted-foreground text-xs">
                {"authorName" in entry.value ? entry.value.authorName : ""}
              </span>
            </div>
          ) : (
            <div
              className={`rounded-md border border-border p-2 ${
                entry.value.sender === "operator" ? "ml-8 bg-surface" : "mr-8"
              }`}
              key={`message-${entry.value.id}`}
            >
              <div className="flex items-baseline justify-between gap-2">
                <span className="font-mono text-[10px] uppercase tracking-[0.12em]">
                  {translate(`sender.${entry.value.sender}`)}
                  {"authorName" in entry.value && entry.value.authorName
                    ? ` · ${entry.value.authorName}`
                    : ""}
                </span>
                <span className="flex items-center gap-2">
                  {entry.value.sender === "operator" && "delivered" in entry.value && (
                    <span className="text-[10px] text-muted-foreground">
                      {translate(entry.value.delivered ? "delivered" : "queued")}
                    </span>
                  )}
                  <span className="font-mono text-[11px] text-muted-foreground">
                    {new Date(entry.value.createdAt).toLocaleString(locale)}
                  </span>
                </span>
              </div>
              <p className="whitespace-pre-wrap text-sm">{entry.value.body}</p>
            </div>
          ),
        )}
      </div>

      {writable && (
        <ReplyBox
          onSent={() => {
            void mutate();
            onChanged();
          }}
          ticketId={ticketId}
        />
      )}
    </Card>
  );
}

/** Assignment, queue, priority, status, tags, and merge. */
function TicketControls({
  onChanged,
  queues,
  tags,
  ticket,
}: {
  onChanged: () => void;
  queues: SupportQueue[];
  tags: SupportTag[];
  ticket: TicketDetail["ticket"];
}) {
  const translate = useTranslations("admin.support");
  const { run, pending } = useOperatorAction();
  const { session } = useSession();
  const [mergeTarget, setMergeTarget] = useState("");

  async function post(path: string, body: Record<string, unknown>) {
    const ok = await run(`/v1/panel/support/tickets/${ticket.id}/${path}`, {
      body,
      method: "POST",
    });
    if (ok) {
      onChanged();
    }
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button
        disabled={pending}
        onClick={() => post("assign", { assigneeId: ticket.assigneeId ? "" : session?.account.id })}
        size="sm"
        variant="outline"
      >
        {translate(ticket.assigneeId ? "release" : "takeIt")}
      </Button>

      <select
        className="h-8 rounded-md border border-border bg-transparent px-2 text-xs"
        onChange={(event) => post("queue", { queueId: event.target.value })}
        value={ticket.queueId}
      >
        {queues.map((queue) => (
          <option key={queue.id} value={queue.id}>
            {queue.nameEn}
          </option>
        ))}
      </select>

      <select
        className="h-8 rounded-md border border-border bg-transparent px-2 text-xs"
        onChange={(event) => post("priority", { priority: event.target.value })}
        value={ticket.priority}
      >
        {PRIORITIES.map((priority) => (
          <option key={priority} value={priority}>
            {translate(`priority.${priority}`)}
          </option>
        ))}
      </select>

      <select
        className="h-8 rounded-md border border-border bg-transparent px-2 text-xs"
        onChange={(event) => post("status", { status: event.target.value })}
        value={ticket.status}
      >
        {STATUSES.map((status) => (
          <option key={status} value={status}>
            {translate(`status.${status}`)}
          </option>
        ))}
      </select>

      <select
        className="h-8 rounded-md border border-border bg-transparent px-2 text-xs"
        onChange={async (event) => {
          const code = event.target.value;
          if (!code) {
            return;
          }
          const attached = ticket.tags.includes(code);
          const ok = await run(`/v1/panel/support/tickets/${ticket.id}/tags/${code}`, {
            method: attached ? "DELETE" : "PUT",
          });
          if (ok) {
            onChanged();
          }
        }}
        value=""
      >
        <option value="">{translate("tagAction")}</option>
        {tags.map((tag) => (
          <option key={tag.code} value={tag.code}>
            {ticket.tags.includes(tag.code) ? `− ${tag.nameEn}` : `+ ${tag.nameEn}`}
          </option>
        ))}
      </select>

      {/* Merging is scoped to one customer by the server. Putting one
          customer's words into another's conversation is refused rather than
          warned about. */}
      <span className="flex items-center gap-1">
        <Input
          className="h-8 w-56 text-xs"
          onChange={(event) => setMergeTarget(event.target.value)}
          placeholder={translate("mergeInto")}
          value={mergeTarget}
        />
        <Button
          disabled={pending || mergeTarget.trim().length === 0}
          onClick={async () => {
            await post("merge", { survivorId: mergeTarget.trim() });
            setMergeTarget("");
          }}
          size="sm"
          variant="ghost"
        >
          {translate("merge")}
        </Button>
      </span>
    </div>
  );
}

/**
 * The reply box, and the internal-note box beside it.
 *
 * They are separate controls with separate buttons rather than one box with a
 * toggle. A toggle is a state somebody can be wrong about, and being wrong here
 * means sending the customer a note about them.
 */
function ReplyBox({ onSent, ticketId }: { onSent: () => void; ticketId: string }) {
  const translate = useTranslations("admin.support");
  const [body, setBody] = useState("");
  const [note, setNote] = useState("");
  const [cannedId, setCannedId] = useState("");
  const { run, pending, error } = useOperatorAction();
  const reply = useSubmission();

  const { data: canned } = useSWR<Listing<CannedResponse>, ApiError>(
    "/v1/panel/support/canned",
    fetcher,
  );

  return (
    <div className="flex flex-col gap-2 border-border border-t pt-3">
      {error && <p className="text-danger-foreground text-sm">{error.message}</p>}

      <div className="flex flex-wrap items-center gap-2">
        <select
          className="h-8 rounded-md border border-border bg-transparent px-2 text-xs"
          onChange={(event) => {
            const response = (canned?.items ?? []).find((item) => item.id === event.target.value);
            setCannedId(event.target.value);
            if (response) {
              setBody(response.bodyEn);
            }
          }}
          value={cannedId}
        >
          <option value="">{translate("insertCanned")}</option>
          {(canned?.items ?? []).map((response) => (
            <option key={response.id} value={response.id}>
              {response.titleEn}
            </option>
          ))}
        </select>
        <span className="text-muted-foreground text-xs">{translate("cannedHint")}</span>
      </div>

      <textarea
        className="min-h-20 rounded-md border border-border bg-transparent p-2 text-sm"
        onChange={(event) => setBody(event.target.value)}
        placeholder={translate("replyPlaceholder")}
        value={body}
      />
      <div className="flex flex-wrap items-center gap-2">
        <Button
          disabled={pending || body.trim().length === 0}
          onClick={async () => {
            // The key is required by the API and is the message's dedupe key:
            // a double-clicked send reaches the reply that already exists
            // rather than answering the customer twice.
            const ok = await run(`/v1/panel/support/tickets/${ticketId}/reply`, {
              body: { body: body.trim(), cannedResponseId: cannedId || undefined },
              method: "POST",
              submission: reply,
            });
            if (ok) {
              setBody("");
              setCannedId("");
              onSent();
            }
          }}
          size="sm"
        >
          {translate("send")}
        </Button>
        <span className="text-muted-foreground text-xs">{translate("sendHint")}</span>
      </div>

      <textarea
        className="min-h-14 rounded-md border border-warning-border border-dashed bg-warning-surface p-2 text-sm"
        onChange={(event) => setNote(event.target.value)}
        placeholder={translate("notePlaceholder")}
        value={note}
      />
      <Button
        className="self-start"
        disabled={pending || note.trim().length === 0}
        onClick={async () => {
          const ok = await run(`/v1/panel/support/tickets/${ticketId}/notes`, {
            body: { body: note.trim() },
            method: "POST",
          });
          if (ok) {
            setNote("");
            onSent();
          }
        }}
        size="sm"
        variant="outline"
      >
        {translate("addNote")}
      </Button>
    </div>
  );
}
