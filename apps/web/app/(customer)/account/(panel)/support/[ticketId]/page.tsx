"use client";

import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Input, Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { toast } from "@omniflow/ui/toast";
import { ArrowRight, Paperclip } from "lucide-react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useFormatter, useTranslations } from "next-intl";
import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import useSWR, { useSWRConfig } from "swr";

import { AccountNotice, ListSkeleton } from "@/components/account/state";
import { AttachmentLimits } from "@/components/account/support/composer";
import { ConversationTurns } from "@/components/account/support/conversation";
import { idempotentHeaders, useIdempotencyKey } from "@/components/account/support/idempotency";
import { useProblemMessage } from "@/components/account/support/problem";
import { TicketStatusBadge } from "@/components/account/support/ticket-row";
import {
  SUPPORT_LIMITS_KEY,
  type SupportConversation,
  type SupportLimits,
} from "@/components/account/support/types";
import { rejectAttachment, uploadSupportAttachment } from "@/components/account/support/upload";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useBytes } from "@/lib/format";

/** Every cached page of the ticket list, so a change here is reflected there. */
function isTicketListKey(key: unknown): boolean {
  return typeof key === "string" && key.startsWith("/v1/account/support/tickets?");
}

/**
 * One conversation.
 *
 * The screen's job beyond showing the thread is to make the state of the
 * conversation legible before the customer types: a closed ticket offers a
 * reopen rather than a composer that would be refused, and a merged one points at
 * where its answer will actually arrive rather than showing a thread that has
 * stopped moving for reasons nothing on the page explains.
 */
export default function SupportTicketPage() {
  const translate = useTranslations("account.support");
  const format = useFormatter();
  const describeProblem = useProblemMessage();
  const { ticketId } = useParams<{ ticketId: string }>();
  const { mutate: mutateGlobal } = useSWRConfig();
  const conversationKey = `/v1/account/support/tickets/${ticketId}`;
  // The thread polls while it is still alive, so an operator's reply or a
  // system notice appears without a reload; a closed or merged conversation is
  // finished and is read once. Fifteen seconds matches the desk's own refresh.
  const { data, error, isLoading, mutate } = useSWR<SupportConversation, ApiError>(
    conversationKey,
    fetcher,
    {
      refreshInterval: (latest) =>
        latest && (latest.ticket.status === "closed" || latest.ticket.status === "merged")
          ? 0
          : 15_000,
    },
  );
  const { data: limits } = useSWR<SupportLimits, ApiError>(SUPPORT_LIMITS_KEY, fetcher);

  const [busy, setBusy] = useState(false);
  const [confirmClose, setConfirmClose] = useState(false);
  const [divider, setDivider] = useState<number | null>(null);
  const opened = useRef<string | null>(null);

  /**
   * Opening the conversation marks it read — on the server, for every surface.
   *
   * Read state is a property of the message rather than of this screen, which is
   * why nothing here keeps its own idea of what has been seen: the same reply
   * read in Telegram arrives here already read, and this call is what makes the
   * reverse true. The divider is the one thing derived locally, and it is only a
   * record of what the server said was unread at the moment the page opened.
   */
  useEffect(() => {
    if (!data || opened.current === ticketId) {
      return;
    }
    opened.current = ticketId;
    const oldestUnread = data.messages.find((message) => message.unread);
    setDivider(oldestUnread?.id ?? null);
    if (data.ticket.unreadCount === 0) {
      return;
    }
    apiFetch(`${conversationKey}/read`, { method: "POST" })
      .then(() => mutateGlobal(isTicketListKey))
      // A failed read is not worth a toast. The customer came to read the reply,
      // they have it, and the badge will clear on the next successful open.
      .catch(() => undefined);
  }, [conversationKey, data, mutateGlobal, ticketId]);

  async function runTicketAction(action: "close" | "reopen") {
    setBusy(true);
    try {
      await apiFetch(`${conversationKey}/${action}`, { method: "POST" });
      await mutate();
      await mutateGlobal(isTicketListKey);
      toast.success(translate(action === "close" ? "ticket.closed" : "ticket.reopened"));
    } catch (actionError) {
      toast.error(describeProblem(actionError));
    } finally {
      setBusy(false);
      setConfirmClose(false);
    }
  }

  if (isLoading) {
    return <ListSkeleton rows={3} />;
  }
  if (error || !data) {
    const missing = error?.status === 404;
    return (
      <AccountNotice
        action={
          missing ? (
            <Button asChild>
              <Link href="/account/support">{translate("tickets.title")}</Link>
            </Button>
          ) : (
            <Button onClick={() => mutate()}>{translate("actions.retry")}</Button>
          )
        }
        description={
          missing ? translate("problem.not_found") : translate("ticket.errorDescription")
        }
        title={translate("ticket.error")}
        variant={error?.status === 503 ? "offline" : "danger"}
      />
    );
  }

  const { messages, ticket } = data;

  return (
    <div className="animate-step-in space-y-4">
      <header className="space-y-2">
        <h1 className="font-semibold text-[18px] leading-snug tracking-[-0.02em]">
          {ticket.subject}
        </h1>
        <div className="flex flex-wrap items-center gap-2">
          <TicketStatusBadge status={ticket.status} />
          <span className="font-mono text-[11px] text-subtle-foreground">
            {translate("ticket.opened", {
              date: format.dateTime(new Date(ticket.createdAt), {
                day: "numeric",
                month: "short",
                year: "numeric",
              }),
            })}
          </span>
        </div>
      </header>

      {ticket.mergedIntoTicketId && (
        <div
          className="space-y-2 rounded-lg border border-info/40 bg-info/10 px-4 py-3"
          role="status"
        >
          <p className="font-semibold text-[13.5px]">{translate("ticket.merged")}</p>
          <p className="text-[12.5px] leading-relaxed">{translate("ticket.mergedDescription")}</p>
          <Button asChild size="sm" variant="outline">
            <Link href={`/account/support/${ticket.mergedIntoTicketId}`}>
              {translate("ticket.mergedLink")}
              <ArrowRight aria-hidden />
            </Link>
          </Button>
        </div>
      )}

      {messages.length === 0 ? (
        <AccountNotice
          description={translate("ticket.emptyDescription")}
          title={translate("ticket.empty")}
        />
      ) : (
        <ConversationTurns firstUnreadId={divider} messages={messages} />
      )}

      {ticket.status === "resolved" && ticket.canReply && (
        <p className="rounded-lg border border-border bg-card px-4 py-3 text-[12.5px] text-muted-foreground leading-relaxed">
          {translate("ticket.resolvedHint")}
        </p>
      )}

      {ticket.canReply ? (
        <div className="space-y-5">
          <ReplyForm conversationKey={conversationKey} onSent={mutate} />
          {limits && (
            <AttachmentForm
              limits={limits}
              onSent={async () => {
                // An upload is a message: it moves the ticket in the list the
                // same way a typed reply does, so the list is revalidated too.
                await mutate();
                await mutateGlobal(isTicketListKey);
              }}
              ticketId={ticketId}
            />
          )}
        </div>
      ) : (
        !ticket.mergedIntoTicketId && (
          <div className="space-y-3 rounded-lg border border-border bg-card px-4 py-3.5">
            <p className="font-semibold text-[13.5px]">{translate("ticket.cannotReply")}</p>
            <p className="text-[12.5px] text-muted-foreground leading-relaxed">
              {translate("ticket.cannotReplyDescription")}
            </p>
            <Button disabled={busy} onClick={() => runTicketAction("reopen")} size="sm">
              {translate("ticket.reopen")}
            </Button>
          </div>
        )
      )}

      {ticket.open && (
        <Button
          className="w-full text-destructive"
          disabled={busy}
          onClick={() => setConfirmClose(true)}
          size="lg"
          variant="outline"
        >
          {translate("ticket.close")}
        </Button>
      )}

      <ConfirmDialog
        cancelLabel={translate("actions.cancel")}
        confirmLabel={translate("ticket.close")}
        description={translate("ticket.closeConfirmDescription")}
        destructive
        onConfirm={() => runTicketAction("close")}
        onOpenChange={setConfirmClose}
        open={confirmClose}
        pending={busy}
        title={translate("ticket.closeConfirm")}
      />
    </div>
  );
}

/**
 * The reply box.
 *
 * It is a form rather than a textarea with a button beside it so that Enter
 * behaves the way the platform expects and so that the browser's own validation
 * and autofill machinery applies. Send stays disabled on empty input rather than
 * posting a blank turn the operator would have to interpret.
 */
function ReplyForm({
  conversationKey,
  onSent,
}: {
  conversationKey: string;
  onSent: () => Promise<unknown>;
}) {
  const translate = useTranslations("account.support");
  const describeProblem = useProblemMessage();
  const { mutate: mutateGlobal } = useSWRConfig();
  const idempotencyKey = useIdempotencyKey();
  const fieldId = useId();
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const body = message.trim();
    if (!body) {
      return;
    }
    setBusy(true);
    try {
      await apiFetch(`${conversationKey}/messages`, {
        body: JSON.stringify({ message: body }),
        headers: idempotentHeaders(idempotencyKey(body)),
        method: "POST",
      });
      setMessage("");
      await onSent();
      await mutateGlobal(isTicketListKey);
      toast.success(translate("ticket.sent"));
    } catch (replyError) {
      toast.error(describeProblem(replyError));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="space-y-2" onSubmit={submit}>
      <Label htmlFor={fieldId}>{translate("ticket.reply")}</Label>
      <Textarea
        className="min-h-24"
        disabled={busy}
        id={fieldId}
        onChange={(event) => setMessage(event.target.value)}
        placeholder={translate("ticket.replyPlaceholder")}
        value={message}
      />
      <Button className="w-full" disabled={busy || !message.trim()} size="lg" type="submit">
        {busy ? translate("ticket.sending") : translate("ticket.send")}
      </Button>
    </form>
  );
}

/**
 * The attachment control.
 *
 * The limits are stated above the picker, not discovered from a refusal. The
 * same two rules are then applied in the browser before anything is sent — the
 * server checks them again, because nothing in a browser is a boundary, but
 * checking here is what turns "your upload failed" into a sentence naming this
 * file's size and this installation's cap, said before a slow connection spends a
 * minute on it.
 */
function AttachmentForm({
  limits,
  onSent,
  ticketId,
}: {
  limits: SupportLimits;
  onSent: () => Promise<unknown>;
  ticketId: string;
}) {
  const translate = useTranslations("account.support");
  const describeProblem = useProblemMessage();
  const formatBytes = useBytes();
  const idempotencyKey = useIdempotencyKey();
  const fileId = useId();
  const noteId = useId();
  // The picker is remounted rather than reset through a ref: the shared Input
  // primitive takes no ref, and a file input only fires `change` when the value
  // actually differs — so a customer who shrinks a rejected file and picks the
  // same name again would otherwise get no event at all.
  const [pickerKey, setPickerKey] = useState(0);
  const [file, setFile] = useState<File | null>(null);
  const [note, setNote] = useState("");
  const [rejection, setRejection] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function chooseFile(chosen: File | null) {
    setRejection(null);
    if (!chosen) {
      setFile(null);
      return;
    }
    const refusal = rejectAttachment(chosen, limits);
    if (refusal) {
      setFile(null);
      setPickerKey((generation) => generation + 1);
      setRejection(
        refusal.reason === "too_large"
          ? translate("attachments.tooLargeLocal", {
              limit: formatBytes(limits.maxAttachmentBytes),
              size: formatBytes(chosen.size),
            })
          : translate("attachments.mediaTypeLocal", {
              type: chosen.type || "unknown",
              types: limits.allowedMediaTypes.join(", "),
            }),
      );
      return;
    }
    setFile(chosen);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!file) {
      return;
    }
    setBusy(true);
    try {
      // The key is derived from the file and the note, so a double-tapped Send
      // or a replayed form reaches the message that already exists, while a
      // different file or a rewritten note is a new upload.
      await uploadSupportAttachment(
        ticketId,
        file,
        note,
        idempotencyKey(`${file.name}|${file.size}|${file.lastModified}|${note.trim()}`),
      );
      setFile(null);
      setNote("");
      setPickerKey((generation) => generation + 1);
      await onSent();
      toast.success(translate("attachments.uploaded"));
    } catch (uploadError) {
      toast.error(describeProblem(uploadError));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="space-y-2.5 rounded-lg border border-border bg-card p-4" onSubmit={submit}>
      <div className="flex items-center gap-2">
        <Paperclip aria-hidden className="size-[15px] text-muted-foreground" />
        <h2 className="font-semibold text-[14px]">{translate("attachments.title")}</h2>
      </div>
      <AttachmentLimits limits={limits} />

      <Label className="sr-only" htmlFor={fileId}>
        {translate("attachments.choose")}
      </Label>
      <Input
        accept={limits.allowedMediaTypes.join(",")}
        aria-describedby={rejection ? `${fileId}-error` : undefined}
        aria-invalid={rejection ? true : undefined}
        disabled={busy}
        id={fileId}
        key={pickerKey}
        onChange={(event) => chooseFile(event.target.files?.[0] ?? null)}
        type="file"
      />
      {rejection && (
        <p
          className="text-[12px] text-destructive leading-relaxed"
          id={`${fileId}-error`}
          role="alert"
        >
          {rejection}
        </p>
      )}
      {file && (
        <p className="font-mono text-[11px] text-subtle-foreground">
          {translate("attachments.selected", { name: file.name, size: formatBytes(file.size) })}
        </p>
      )}

      <Label className="pt-1" htmlFor={noteId}>
        {translate("attachments.note")}
      </Label>
      <Textarea
        className="min-h-16"
        disabled={busy}
        id={noteId}
        maxLength={limits.maxMessageLength}
        onChange={(event) => setNote(event.target.value)}
        value={note}
      />

      <Button disabled={busy || !file} size="sm" type="submit">
        {busy ? translate("attachments.uploading") : translate("attachments.upload")}
      </Button>
    </form>
  );
}
