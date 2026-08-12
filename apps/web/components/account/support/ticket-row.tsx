"use client";

import { Badge } from "@omniflow/ui/badge";
import { ChevronRight } from "lucide-react";
import Link from "next/link";
import { useFormatter, useTranslations } from "next-intl";

import type { SupportTicket, TicketStatus } from "./types";

/**
 * Which tone each status renders in.
 *
 * The mapping lives in one place so the pill in the list and the pill at the top
 * of the conversation can never disagree about what a status means. `pending` is
 * a warning tone rather than a neutral one because, in this vocabulary, it means
 * the customer is waiting on us — the one state where the colour is a promise
 * being tracked rather than a label.
 */
const STATUS_TONE: Record<TicketStatus, "info" | "neutral" | "outline" | "success" | "warning"> = {
  closed: "neutral",
  merged: "outline",
  open: "info",
  pending: "warning",
  resolved: "success",
};

export function TicketStatusBadge({ status }: { status: TicketStatus }) {
  const translate = useTranslations("account.support");
  return <Badge variant={STATUS_TONE[status]}>{translate(`status.${status}`)}</Badge>;
}

/**
 * One conversation, as the list shows it.
 *
 * The whole row is the link rather than a title inside it: on a phone the row is
 * the target the thumb is aiming at, and a link that occupies less than the thing
 * it looks like is a link that gets missed.
 */
export function TicketRow({ ticket }: { ticket: SupportTicket }) {
  const translate = useTranslations("account.support");
  const format = useFormatter();
  const unread = ticket.unreadCount > 0;

  return (
    <li>
      <Link
        className="flex items-center gap-3 px-4 py-3.5 transition-colors hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring focus-visible:-outline-offset-2"
        href={`/account/support/${ticket.id}`}
      >
        <div className="min-w-0 flex-1 space-y-1.5">
          <div className="flex items-center gap-2">
            {/* The dot is decorative: the unread count is spelled out below it,
                so a reader that cannot see colour is not told less. */}
            {unread && <span aria-hidden className="size-[7px] shrink-0 rounded-full bg-primary" />}
            <span className="truncate font-semibold text-[15px]">{ticket.subject}</span>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <TicketStatusBadge status={ticket.status} />
            {unread && (
              <span className="font-medium font-mono text-[11px] text-primary">
                {translate("tickets.unread", { count: ticket.unreadCount })}
              </span>
            )}
          </div>
          <p className="font-mono text-[11px] text-subtle-foreground">
            {`${format.relativeTime(new Date(ticket.lastMessageAt))} · ${translate("tickets.messages", { count: ticket.messageCount })}`}
          </p>
        </div>
        <ChevronRight aria-hidden className="size-4 shrink-0 text-subtle-foreground" />
      </Link>
    </li>
  );
}
