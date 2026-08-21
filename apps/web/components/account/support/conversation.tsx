"use client";

import { cn } from "@omniflow/ui/lib/utils";
import { toast } from "@omniflow/ui/toast";
import { Download, FileText, ImageIcon, Send } from "lucide-react";
import { useFormatter, useTranslations } from "next-intl";
import { useState } from "react";

import { useBytes } from "@/lib/format";

import { downloadSupportAttachment } from "./download";
import { useProblemMessage } from "./problem";
import type { SupportAttachment, SupportMessage } from "./types";

/**
 * The turns of one conversation.
 *
 * Three things carry who said what, because any one of them alone fails
 * somebody: the alignment and tint are for a glance, the author's name is
 * written out for a reader who cannot see either, and the list is an ordered one
 * so the sequence survives being read out of order. A chat rendered only as
 * coloured bubbles is a chat that is unusable with a screen reader.
 *
 * A system turn is neither side's: it is a full-width note about something that
 * happened to the conversation, so it is centred and quiet rather than dressed as
 * a message from a person.
 */
export function ConversationTurns({
  firstUnreadId,
  messages,
}: {
  /**
   * The oldest message that was still unread when this screen opened. It draws a
   * divider and nothing else — read state itself belongs to the server, which is
   * told the conversation was opened and decides what counts as read from there.
   */
  firstUnreadId: number | null;
  messages: SupportMessage[];
}) {
  const translate = useTranslations("account.support");

  return (
    <ol aria-label={translate("ticket.conversation")} className="space-y-3">
      {messages.map((message) => (
        <li className="space-y-3" key={message.id}>
          {/* The rules either side are decoration and are hidden; the sentence
              between them is real content and is read out in sequence, which is
              what tells a screen-reader user where the new turns begin. */}
          {firstUnreadId === message.id && (
            <p className="flex items-center gap-3 font-medium font-mono text-[10px] text-primary uppercase tracking-[0.12em]">
              <span aria-hidden className="h-px flex-1 bg-primary/40" />
              {translate("ticket.newDivider")}
              <span aria-hidden className="h-px flex-1 bg-primary/40" />
            </p>
          )}
          <ConversationTurn message={message} />
        </li>
      ))}
    </ol>
  );
}

function ConversationTurn({ message }: { message: SupportMessage }) {
  const translate = useTranslations("account.support");
  const format = useFormatter();
  const mine = message.author === "customer";
  const system = message.author === "system";

  if (system) {
    return (
      <p className="px-6 py-1 text-center font-mono text-[11px] text-subtle-foreground leading-relaxed">
        {message.body}
      </p>
    );
  }

  return (
    <article
      className={cn(
        "max-w-[85%] space-y-2 rounded-lg border px-3.5 py-3",
        mine ? "ml-auto border-primary/25 bg-primary/10" : "border-border bg-card",
      )}
    >
      <header className="flex items-baseline justify-between gap-3">
        <span className="font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.12em]">
          {translate(`author.${message.author}`)}
        </span>
        <time
          className="shrink-0 font-mono text-[10.5px] text-subtle-foreground"
          dateTime={message.createdAt}
        >
          {format.dateTime(new Date(message.createdAt), {
            day: "numeric",
            hour: "2-digit",
            minute: "2-digit",
            month: "short",
          })}
        </time>
      </header>
      {/* The body is plain text written by a person and is rendered as such.
          `whitespace-pre-wrap` keeps their line breaks; nothing is interpreted as
          markup, which is what keeps an operator's reply from ever becoming one. */}
      {message.body && (
        <p className="whitespace-pre-wrap text-[14px] leading-relaxed">{message.body}</p>
      )}
      {message.attachments.length > 0 && (
        <ul className="space-y-1.5">
          {message.attachments.map((attachment) => (
            <li key={attachment.id}>
              <AttachmentRow attachment={attachment} />
            </li>
          ))}
        </ul>
      )}
    </article>
  );
}

/**
 * One attached file.
 *
 * A file that arrived through Telegram is shown as what it is and given no
 * download control at all. Omniflow holds the reference, not the bytes, so a
 * button here could only ever fail — and a control that cannot work is worse
 * than its absence, because it also costs the customer a click to find out.
 */
function AttachmentRow({ attachment }: { attachment: SupportAttachment }) {
  const translate = useTranslations("account.support");
  const describeProblem = useProblemMessage();
  const formatBytes = useBytes();
  const [busy, setBusy] = useState(false);
  const Icon = attachment.kind === "photo" ? ImageIcon : FileText;
  const name = attachment.fileName || translate("attachments.unnamed");

  if (!attachment.downloadable) {
    return (
      <div className="flex items-start gap-2 rounded-md border border-border border-dashed px-2.5 py-2">
        <Send aria-hidden className="mt-0.5 size-[13px] shrink-0 text-subtle-foreground" />
        <div className="min-w-0">
          <p className="truncate font-medium text-[12.5px]">{name}</p>
          <p className="mt-0.5 text-[11.5px] text-muted-foreground leading-relaxed">
            <span className="font-mono text-subtle-foreground">
              {translate("attachments.remote")}
            </span>
            {" — "}
            {translate("attachments.remoteDescription")}
          </p>
        </div>
      </div>
    );
  }

  // A button rather than a link: the file is fetched through the API so a
  // refusal — the file was purged, storage is down — is shown as a message
  // instead of being saved to disk as a problem document named like the file.
  return (
    <button
      className="flex w-full items-center gap-2 rounded-md border border-border px-2.5 py-2 text-left transition-colors hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2 disabled:opacity-60"
      disabled={busy}
      onClick={async () => {
        setBusy(true);
        try {
          await downloadSupportAttachment(attachment.id, name);
        } catch (downloadError) {
          toast.error(describeProblem(downloadError));
        } finally {
          setBusy(false);
        }
      }}
      type="button"
    >
      <Icon aria-hidden className="size-[13px] shrink-0 text-muted-foreground" />
      <span className="min-w-0 flex-1 truncate font-medium text-[12.5px]">{name}</span>
      <span className="shrink-0 font-mono text-[10.5px] text-subtle-foreground">
        {formatBytes(attachment.sizeBytes)}
      </span>
      <Download aria-hidden className="size-[13px] shrink-0 text-subtle-foreground" />
      <span className="sr-only">
        {busy ? translate("attachments.downloading") : translate("attachments.download")}
      </span>
    </button>
  );
}
