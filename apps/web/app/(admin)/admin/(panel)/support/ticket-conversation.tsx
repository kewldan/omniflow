"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import {
  Command,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@omniflow/ui/command";
import { Textarea } from "@omniflow/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@omniflow/ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { Skeleton } from "@omniflow/ui/skeleton";
import { toast } from "@omniflow/ui/toast";
import {
  CircleDot,
  Flag,
  GitMerge,
  Inbox,
  RotateCcw,
  Tag,
  UserCheck,
  UserMinus,
  UserRound,
} from "lucide-react";
import { useLocale, useTranslations } from "next-intl";
import { useEffect, useRef, useState } from "react";
import useSWR, { mutate as revalidateKey } from "swr";

import { CustomerChip } from "@/components/admin/customer-chip";
import { StateNotice } from "@/components/admin/state-notice";
import { VoicePlayer } from "@/components/admin/voice-player";
import { Timestamp } from "@/components/intl/timestamp";
import { ApiError, apiFetch, fetcher } from "@/lib/api";
import { downloadThroughApi } from "@/lib/download";
import { useBytes } from "@/lib/format";
import { useSubmission } from "@/lib/idempotency";
import { localized } from "@/lib/localized";
import { type Listing, type Page, useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";
import type {
  CannedResponse,
  DeliveryState,
  SupportAttachment,
  SupportQueue,
  SupportTag,
  SupportTicket,
  TicketDetail,
} from "./types";

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
    // While the bot is still copying a file the customer just sent, read the
    // ticket often, so the photo or the voice message appears within seconds
    // of the copy rather than on the next quarter-minute.
    { refreshInterval: (latest) => (awaitingCopy(latest) ? 3_000 : 15_000) },
  );
  const { data: tags } = useSWR<Listing<SupportTag>, ApiError>("/v1/panel/support/tags", fetcher);

  // The newest entry is the one an operator is reading or has just written, so
  // the thread opens at its end and follows a reply or note once it lands.
  // Only the thread's own box scrolls; the page stays where it is.
  const thread = useRef<HTMLDivElement>(null);
  const entries = (data?.messages.length ?? 0) + (data?.notes.length ?? 0);
  useEffect(() => {
    const box = thread.current;
    if (box && entries > 0 && ticketId) {
      box.scrollTop = box.scrollHeight;
    }
  }, [entries, ticketId]);

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

  // On a wide screen the conversation takes the window's height, so the
  // thread is the part that grows; it used to be capped at 42rem, and the
  // header and reply box above and below left the thread a few lines.
  return (
    <Card className="flex flex-col gap-3 p-4 lg:sticky lg:top-18 lg:h-[calc(100dvh-5.5rem)] lg:min-h-[36rem]">
      <header className="flex flex-col gap-2 border-border border-b pb-3">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <h2 className="font-semibold">{ticket.subject || translate("noSubject")}</h2>
          <CustomerChip id={ticket.customerId} />
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <Badge className="gap-1" variant="neutral">
            <CircleDot aria-hidden className="size-3" />
            {translate(`status.${ticket.status}`)}
          </Badge>
          <Badge className="gap-1" variant={ticket.priority === "urgent" ? "warning" : "neutral"}>
            <Flag aria-hidden className="size-3" />
            {translate(`priority.${ticket.priority}`)}
          </Badge>
          <span className="inline-flex items-center gap-1 text-muted-foreground">
            <Inbox aria-hidden className="size-3.5" />
            {(() => {
              const queue = queues.find((entry) => entry.code === ticket.queueCode);
              return queue
                ? localized(locale, queue.nameEn, queue.nameRu, queue.code)
                : ticket.queueCode;
            })()}
          </span>
          <span className="inline-flex items-center gap-1 text-muted-foreground">
            <UserRound aria-hidden className="size-3.5" />
            {ticket.assigneeName || translate("nobody")}
          </span>
          {ticket.reopenedCount > 0 && (
            // Reopens are the signal that an answer did not actually answer the
            // question, which is worth showing beside the ticket rather than
            // only in a report.
            <Badge className="gap-1" variant="warning">
              <RotateCcw aria-hidden className="size-3" />
              {translate("reopened", { count: ticket.reopenedCount })}
            </Badge>
          )}
          {ticket.mergedIntoTicketId && <Badge variant="neutral">{translate("mergedAway")}</Badge>}
        </div>
        {writable && (
          <TicketControls
            onChanged={() => {
              void mutate();
              refreshNavigationCounts();
              onChanged();
            }}
            queues={queues}
            tags={tags?.items ?? []}
            ticket={ticket}
          />
        )}
      </header>

      <div
        className="flex min-h-64 flex-1 flex-col gap-3 overflow-y-auto max-lg:max-h-[65dvh] lg:min-h-24"
        ref={thread}
      >
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
                  <Timestamp value={entry.value.createdAt} />
                </span>
              </div>
              <p
                className={`whitespace-pre-wrap text-sm ${
                  "deletedAt" in entry.value && entry.value.deletedAt
                    ? "text-muted-foreground line-through"
                    : ""
                }`}
              >
                {entry.value.body}
              </p>
              {"deletedAt" in entry.value && entry.value.deletedAt ? (
                <span className="text-muted-foreground text-xs">
                  {translate("deletedForCustomer")}
                </span>
              ) : writable && "sender" in entry.value ? (
                <Button
                  className="h-6 px-2 text-xs"
                  onClick={async () => {
                    try {
                      await apiFetch(
                        `/v1/panel/support/tickets/${ticket.id}/messages/${entry.value.id}`,
                        { method: "DELETE" },
                      );
                      void mutate();
                    } catch {
                      toast.error(translate("deleteFailed"));
                    }
                  }}
                  size="sm"
                  variant="ghost"
                >
                  {translate("deleteForCustomer")}
                </Button>
              ) : null}
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
                  {"delivery" in entry.value && entry.value.delivery && (
                    <DeliveryMark
                      reason={entry.value.deliveryReason}
                      state={entry.value.delivery}
                    />
                  )}
                  <span className="font-mono text-[11px] text-muted-foreground">
                    <Timestamp value={entry.value.createdAt} />
                  </span>
                </span>
              </div>
              <p className="whitespace-pre-wrap text-sm">{entry.value.body}</p>
              {"attachments" in entry.value && entry.value.attachments.length > 0 && (
                <ul className="mt-2 space-y-1">
                  {entry.value.attachments.map((attachment) => (
                    <li key={attachment.id}>
                      <AttachmentRow attachment={attachment} ticketId={ticket.id} />
                    </li>
                  ))}
                </ul>
              )}
            </div>
          ),
        )}
      </div>

      {writable && (
        <ReplyBox
          onSent={() => {
            void mutate();
            refreshNavigationCounts();
            onChanged();
          }}
          ticketId={ticketId}
        />
      )}
    </Card>
  );
}

/**
 * The push outcome beside an operator or system message.
 *
 * `undeliverable` and `failed` are coloured and carry the reason, because they
 * change what the operator does next: a customer who blocked the bot has to be
 * told to open the web panel, and nothing about waiting will fix it. The other
 * states are quiet, since they describe the normal course of a push.
 */
function DeliveryMark({ reason, state }: { reason?: string; state: DeliveryState }) {
  const translate = useTranslations("admin.support");
  const attention = state === "undeliverable" || state === "failed";
  const reasonKnown = reason && KNOWN_DELIVERY_REASONS.has(reason);
  return (
    <span
      className={`text-[10px] ${attention ? "text-danger-foreground" : "text-muted-foreground"}`}
      title={reasonKnown ? translate(`deliveryReason.${reason}`) : reason}
    >
      {translate(`delivery.${state}`)}
      {attention && reason
        ? ` · ${reasonKnown ? translate(`deliveryReason.${reason}`) : reason}`
        : ""}
    </span>
  );
}

/** How long after a file arrives the bot is expected to have copied it. */
const COPY_WINDOW_MS = 3 * 60_000;

/** Whether a Telegram file sent in the last few minutes has no copy yet. */
function awaitingCopy(detail: TicketDetail | undefined): boolean {
  if (!detail) {
    return false;
  }
  const since = Date.now() - COPY_WINDOW_MS;
  return detail.messages.some((message) =>
    message.attachments.some(
      (attachment) =>
        attachment.origin === "telegram" &&
        !attachment.downloadable &&
        new Date(attachment.createdAt).getTime() > since,
    ),
  );
}

/**
 * One file a customer attached.
 *
 * A web upload is downloadable through the API, so a refused download — the
 * file was purged, storage is down — is explained rather than saved to disk as
 * a problem document. A Telegram file is described and not offered: the panel
 * holds a reference, not the bytes, and a button that can only fail is worse
 * than a sentence saying where the file is.
 */
function AttachmentRow({
  attachment,
  ticketId,
}: {
  attachment: SupportAttachment;
  ticketId: string;
}) {
  const translate = useTranslations("admin.support");
  const formatBytes = useBytes();
  const [busy, setBusy] = useState(false);
  const name =
    attachment.fileName ||
    (attachment.kind === "voice"
      ? translate("attachments.voiceMessage")
      : translate("attachments.unnamed"));
  const url = `/v1/panel/support/tickets/${ticketId}/attachments/${attachment.id}`;

  return (
    <div className="flex flex-col gap-2 rounded-md border border-border border-dashed px-2 py-1 text-xs">
      {attachment.kind === "photo" && attachment.downloadable && (
        <PhotoPreview name={name} url={url} />
      )}
      {(attachment.kind === "voice" || attachment.kind === "audio") && attachment.downloadable && (
        <AudioPreview
          mediaType={
            attachment.mediaType || (attachment.kind === "voice" ? "audio/ogg" : "audio/mpeg")
          }
          name={name}
          sizeLabel={formatBytes(attachment.sizeBytes)}
          url={url}
        />
      )}
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-[10px] text-muted-foreground uppercase tracking-[0.12em]">
          {translate(`attachments.kind.${attachment.kind}`)}
        </span>
        <span className="min-w-0 flex-1 truncate">{name}</span>
        <span className="font-mono text-[11px] text-muted-foreground">
          {formatBytes(attachment.sizeBytes)}
        </span>
        {attachment.downloadable ? (
          <Button
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              try {
                await downloadThroughApi(
                  `/v1/panel/support/tickets/${ticketId}/attachments/${attachment.id}`,
                  name,
                );
              } catch (downloadError) {
                toast.error(
                  downloadError instanceof ApiError && downloadError.status === 404
                    ? translate("attachments.gone")
                    : translate("attachments.downloadFailed"),
                );
              } finally {
                setBusy(false);
              }
            }}
            size="sm"
            variant="outline"
          >
            {translate("attachments.download")}
          </Button>
        ) : (
          <span className="text-muted-foreground" title={translate("attachments.telegramHint")}>
            {translate("attachments.telegram")}
          </span>
        )}
      </div>
    </div>
  );
}

/**
 * A customer's photo, shown in the conversation. It is read through the same
 * download route as the button, with the operator's session, and shown from
 * memory; opening it shows it full size in a new tab.
 */
function PhotoPreview({ name, url }: { name: string; url: string }) {
  const translate = useTranslations("admin.support");
  const [source, setSource] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let revoked = false;
    let objectUrl: string | null = null;
    fetch(url, { credentials: "include" })
      .then(async (response) => {
        if (!response.ok) {
          throw new Error(String(response.status));
        }
        // A photo from Telegram is always a JPEG; the stored type may be empty.
        const data = await response.arrayBuffer();
        objectUrl = URL.createObjectURL(new Blob([data], { type: "image/jpeg" }));
        if (!revoked) {
          setSource(objectUrl);
        }
      })
      .catch(() => setFailed(true));
    return () => {
      revoked = true;
      if (objectUrl) {
        URL.revokeObjectURL(objectUrl);
      }
    };
  }, [url]);

  if (failed) {
    return <span className="text-muted-foreground">{translate("attachments.previewFailed")}</span>;
  }
  if (!source) {
    return <span className="h-40 w-56 animate-pulse rounded-md bg-muted" />;
  }
  return (
    <a href={source} rel="noreferrer" target="_blank" title={translate("attachments.openFull")}>
      {/* biome-ignore lint/performance/noImgElement: an in-memory blob the operator's session fetched; next/image cannot load it. */}
      <img
        alt={name}
        className="max-h-72 max-w-full rounded-md border border-border object-contain"
        src={source}
      />
    </a>
  );
}

/**
 * A customer's voice message or audio file, played in the conversation. Like
 * a photo it is read through the download route with the operator's session
 * and played from memory, typed with the stored media type: a Telegram voice
 * message is OGG/Opus, which a browser that cannot play it says so about, and
 * the download button next to it still works.
 */
function AudioPreview({
  mediaType,
  name,
  sizeLabel,
  url,
}: {
  mediaType: string;
  name: string;
  sizeLabel: string;
  url: string;
}) {
  const translate = useTranslations("admin.support");
  const [loaded, setLoaded] = useState<{ source: string; data: ArrayBuffer } | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let revoked = false;
    let objectUrl: string | null = null;
    fetch(url, { credentials: "include" })
      .then(async (response) => {
        if (!response.ok) {
          throw new Error(String(response.status));
        }
        const data = await response.arrayBuffer();
        objectUrl = URL.createObjectURL(new Blob([data], { type: mediaType }));
        if (!revoked) {
          setLoaded({ data, source: objectUrl });
        }
      })
      .catch(() => setFailed(true));
    return () => {
      revoked = true;
      if (objectUrl) {
        URL.revokeObjectURL(objectUrl);
      }
    };
  }, [url, mediaType]);

  if (failed) {
    return <span className="text-muted-foreground">{translate("attachments.playFailed")}</span>;
  }
  if (!loaded) {
    return <span className="h-12 w-80 max-w-full animate-pulse rounded-md bg-muted" />;
  }
  return (
    <VoicePlayer
      data={loaded.data}
      label={name}
      onUnplayable={() => setFailed(true)}
      sizeLabel={sizeLabel}
      source={loaded.source}
    />
  );
}

/** The reason codes the catalogue has words for; anything else is shown raw. */
const KNOWN_DELIVERY_REASONS = new Set([
  "bot_blocked",
  "user_deactivated",
  "chat_not_found",
  "no_telegram",
  "flood_wait",
  "timeout",
  "telegram_unavailable",
  "rejected",
  "not_started",
  "unreachable",
  "account_inactive",
  "delivery_blocked",
  "classification_failed",
]);

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
  const locale = useLocale();
  const { run, pending } = useOperatorAction();
  const { session } = useSession();

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
        {ticket.assigneeId ? (
          <UserMinus aria-hidden className="size-4" />
        ) : (
          <UserCheck aria-hidden className="size-4" />
        )}
        {translate(ticket.assigneeId ? "release" : "takeIt")}
      </Button>

      <Select onValueChange={(value) => post("queue", { queueId: value })} value={ticket.queueId}>
        <SelectTrigger
          aria-label={translate("controls.queue")}
          className="h-8 w-auto gap-1.5 text-xs"
        >
          <Inbox aria-hidden className="size-3.5 text-muted-foreground" />
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {queues.map((queue) => (
            <SelectItem key={queue.id} value={queue.id}>
              {localized(locale, queue.nameEn, queue.nameRu, queue.code)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>

      <Select
        onValueChange={(value) => post("priority", { priority: value })}
        value={ticket.priority}
      >
        <SelectTrigger
          aria-label={translate("controls.priority")}
          className="h-8 w-auto gap-1.5 text-xs"
        >
          <Flag aria-hidden className="size-3.5 text-muted-foreground" />
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {PRIORITIES.map((priority) => (
            <SelectItem key={priority} value={priority}>
              {translate(`priority.${priority}`)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>

      <Select onValueChange={(value) => post("status", { status: value })} value={ticket.status}>
        <SelectTrigger
          aria-label={translate("controls.status")}
          className="h-8 w-auto gap-1.5 text-xs"
        >
          <CircleDot aria-hidden className="size-3.5 text-muted-foreground" />
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {STATUSES.map((status) => (
            <SelectItem key={status} value={status}>
              {translate(`status.${status}`)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>

      {tags.length > 0 && (
        // An action menu rather than a value: it always shows "Tag…", and each
        // entry says whether choosing it adds or removes that tag.
        <Select
          onValueChange={async (code) => {
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
          <SelectTrigger
            aria-label={translate("controls.tags")}
            className="h-8 w-auto gap-1.5 text-xs"
          >
            <Tag aria-hidden className="size-3.5 text-muted-foreground" />
            <SelectValue placeholder={translate("tagAction")} />
          </SelectTrigger>
          <SelectContent>
            {tags.map((tag) => {
              const name = localized(locale, tag.nameEn, tag.nameRu, tag.code);
              return (
                <SelectItem key={tag.code} value={tag.code}>
                  {ticket.tags.includes(tag.code) ? `− ${name}` : `+ ${name}`}
                </SelectItem>
              );
            })}
          </SelectContent>
        </Select>
      )}

      <MergeTicket
        onMerge={(survivorId) => post("merge", { survivorId })}
        pending={pending}
        ticket={ticket}
      />
    </div>
  );
}

/**
 * Merging, behind one button: it is the rare action on a ticket, so it no
 * longer keeps a field open beside the everyday controls.
 *
 * The usual case is a customer who wrote twice, so the popover offers that
 * customer's other tickets first; an identifier can still be pasted for the
 * rest. Merging is scoped to one customer by the server: putting one
 * customer's words into another's conversation is refused rather than warned
 * about.
 */
function MergeTicket({
  onMerge,
  pending,
  ticket,
}: {
  onMerge: (survivorId: string) => Promise<void>;
  pending: boolean;
  ticket: TicketDetail["ticket"];
}) {
  const translate = useTranslations("admin.support");
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const { data } = useSWR<Page<SupportTicket>, ApiError>(
    open ? `/v1/panel/support/tickets?customerId=${ticket.customerId}&pageSize=50` : null,
    fetcher,
  );
  const siblings = (data?.items ?? []).filter(
    (item) => item.id !== ticket.id && item.status !== "merged",
  );
  const needle = query.trim().toLowerCase();
  const shown = needle
    ? siblings.filter(
        (item) =>
          item.subject.toLowerCase().includes(needle) || item.id.toLowerCase().startsWith(needle),
      )
    : siblings;
  // A whole identifier typed by hand is offered even when it is not in the
  // list; the server refuses one that belongs to another customer.
  const pasted =
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(query.trim()) &&
    !shown.some((item) => item.id === query.trim().toLowerCase())
      ? query.trim().toLowerCase()
      : "";

  async function merge(survivorId: string) {
    await onMerge(survivorId);
    setQuery("");
    setOpen(false);
  }

  return (
    <Popover onOpenChange={setOpen} open={open}>
      <PopoverTrigger asChild>
        <Button disabled={pending} size="sm" variant="outline">
          <GitMerge aria-hidden className="size-4" />
          {translate("merge")}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 p-0">
        <Command shouldFilter={false}>
          <CommandInput
            onValueChange={setQuery}
            placeholder={translate("mergeSearch")}
            value={query}
          />
          <CommandList>
            {shown.length === 0 && !pasted ? (
              <p className="px-3 py-6 text-center text-muted-foreground text-xs">
                {!data
                  ? translate("mergeLoading")
                  : siblings.length === 0
                    ? translate("mergeNoSiblings")
                    : translate("mergeNoMatch")}
              </p>
            ) : (
              <CommandGroup heading={translate("mergeTitle")}>
                {shown.map((item) => (
                  <CommandItem
                    disabled={pending}
                    key={item.id}
                    onSelect={() => void merge(item.id)}
                    value={item.id}
                  >
                    <span className="flex min-w-0 flex-col">
                      <span className="truncate">{item.subject || translate("noSubject")}</span>
                      <span className="font-mono text-[11px] text-muted-foreground">
                        {translate(`status.${item.status}`)} ·{" "}
                        <Timestamp mode="date" value={item.lastMessageAt} />
                      </span>
                    </span>
                  </CommandItem>
                ))}
                {pasted && (
                  <CommandItem
                    disabled={pending}
                    onSelect={() => void merge(pasted)}
                    value={pasted}
                  >
                    <span className="truncate font-mono text-xs">
                      {translate("mergeById", { id: pasted.slice(0, 8) })}
                    </span>
                  </CommandItem>
                )}
              </CommandGroup>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
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
  const locale = useLocale();
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
    <div className="shrink-0 flex flex-col gap-2 border-border border-t pt-3">
      {error && <p className="text-danger-foreground text-sm">{error.message}</p>}

      <div className="flex flex-wrap items-center gap-2">
        <Select
          onValueChange={(value) => {
            const response = (canned?.items ?? []).find((item) => item.id === value);
            setCannedId(value);
            if (response) {
              setBody(localized(locale, response.bodyEn, response.bodyRu));
            }
          }}
          value={cannedId}
        >
          <SelectTrigger
            aria-label={translate("insertCanned")}
            className="h-8 w-full text-xs sm:w-64"
            disabled={(canned?.items ?? []).length === 0}
          >
            <SelectValue placeholder={translate("insertCanned")} />
          </SelectTrigger>
          <SelectContent>
            {(canned?.items ?? []).map((response) => (
              <SelectItem key={response.id} value={response.id}>
                {localized(locale, response.titleEn, response.titleRu, response.code)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <span className="text-muted-foreground text-xs">{translate("cannedHint")}</span>
      </div>

      <Textarea
        aria-label={translate("replyPlaceholder")}
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

      <Textarea
        aria-label={translate("notePlaceholder")}
        className="min-h-14 border-warning-border border-dashed bg-warning-surface"
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

/**
 * The sidebar counts tickets waiting on this operator. A reply, a status
 * change or an assignment changes that number at once, so it is asked for
 * again now rather than at the sidebar's next minute.
 */
function refreshNavigationCounts() {
  void revalidateKey("/v1/panel/overview/navigation-counts");
}
