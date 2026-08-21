"use client";

import { Button } from "@omniflow/ui/button";
import { Bell, ChevronRight, Megaphone, Plus } from "lucide-react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import type { ReactNode } from "react";
import useSWR from "swr";
import useSWRInfinite from "swr/infinite";

import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { SupportAlerts } from "@/components/account/support/browser-notifications";
import { TicketRow } from "@/components/account/support/ticket-row";
import {
  SUPPORT_LIMITS_KEY,
  SUPPORT_TICKETS_KEY,
  type SupportLimits,
  type SupportTicketPage,
} from "@/components/account/support/types";
import { type ApiError, fetcher, toQuery } from "@/lib/api";

/**
 * The cursor pages, in the order they were asked for.
 *
 * The first key is written as a literal rather than built, so it is character-for
 * -character the key the notification watcher subscribes to and SWR serves both
 * from one request. Returning null once a page came back without a cursor is what
 * ends the list: the server decides when there is no more, and the panel never
 * guesses from a short page.
 */
function ticketPageKey(index: number, previous: SupportTicketPage | null) {
  if (previous && !previous.nextCursor) {
    return null;
  }
  if (index === 0 || !previous?.nextCursor) {
    return SUPPORT_TICKETS_KEY;
  }
  return `/v1/account/support/tickets${toQuery({ cursor: previous.nextCursor, limit: 20 })}`;
}

/**
 * The support desk's front page.
 *
 * It answers two questions in the order a customer asks them: is there an answer
 * waiting for me, and how do I ask something new. The quota only becomes visible
 * when it starts to matter — a limit announced to somebody with one conversation
 * open is a rule about nothing.
 */
export default function SupportTicketsPage() {
  const translate = useTranslations("account.support");
  const { data, error, isLoading, isValidating, mutate, setSize, size } = useSWRInfinite<
    SupportTicketPage,
    ApiError
  >(ticketPageKey, fetcher);
  // The limits are fetched here as well as on the pages that enforce them: the
  // open-ticket quota is a fact about the list, and the create control has to
  // know it before it is pressed rather than after the request is refused.
  const { data: limits } = useSWR<SupportLimits, ApiError>(SUPPORT_LIMITS_KEY, fetcher);

  const pages = data ?? [];
  const tickets = pages.flatMap((page) => page.items);
  const hasMore = Boolean(pages.at(-1)?.nextCursor);
  const loadingMore = isValidating && pages.length < size;
  const openCount = tickets.filter((ticket) => ticket.open).length;
  // Counted from what has been loaded, which can only understate the total. The
  // server holds the real rule and refuses accordingly; this is the early warning,
  // not the enforcement.
  const quotaReached = Boolean(limits && openCount >= limits.maxOpenTickets);
  // The wording about where a reply arrives follows the account. A customer
  // who signed in by magic link and never opened the bot is not promised a
  // Telegram push that cannot happen; until the first page answers, the
  // narrower promise is the safe one.
  const telegramLinked = pages[0]?.telegramLinked ?? false;

  return (
    <div className="animate-step-in space-y-4">
      <SupportAlerts />

      <header className="space-y-1">
        <h1 className="font-semibold text-[19px] tracking-[-0.02em]">
          {translate("tickets.title")}
        </h1>
        <p className="text-[12.5px] text-muted-foreground leading-relaxed">
          {translate(telegramLinked ? "tickets.subtitle" : "tickets.subtitleWebOnly")}
        </p>
      </header>

      {quotaReached && limits && (
        <div
          className="space-y-1 rounded-lg border border-warning/40 bg-warning/10 px-4 py-3"
          role="status"
        >
          <p className="font-semibold text-[13.5px]">{translate("tickets.quota")}</p>
          <p className="text-[12.5px] leading-relaxed">
            {translate("tickets.quotaDescription", { limit: limits.maxOpenTickets })}
          </p>
        </div>
      )}

      {/* Two branches rather than one control that changes shape: an anchor
          cannot be disabled, and a link that is styled as unavailable while still
          navigating is the worst of both. */}
      {quotaReached ? (
        <Button className="w-full" disabled size="lg">
          <Plus aria-hidden />
          {translate("tickets.new")}
        </Button>
      ) : (
        <Button asChild className="w-full" size="lg">
          <Link href="/account/support/new">
            <Plus aria-hidden />
            {translate("tickets.new")}
          </Link>
        </Button>
      )}

      {isLoading ? (
        <ListSkeleton />
      ) : error ? (
        <AccountNotice
          action={<Button onClick={() => mutate()}>{translate("actions.retry")}</Button>}
          description={translate("tickets.errorDescription")}
          title={translate("tickets.error")}
          variant={error.status === 503 ? "offline" : "danger"}
        />
      ) : tickets.length === 0 ? (
        <AccountNotice
          description={translate(
            telegramLinked ? "tickets.emptyDescription" : "tickets.emptyDescriptionWebOnly",
          )}
          title={translate("tickets.empty")}
        />
      ) : (
        <>
          <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
            {tickets.map((ticket) => (
              <TicketRow key={ticket.id} ticket={ticket} />
            ))}
          </ul>
          {hasMore && (
            <Button
              className="w-full"
              disabled={loadingMore}
              onClick={() => setSize(size + 1)}
              size="lg"
              variant="outline"
            >
              {loadingMore ? translate("tickets.loading") : translate("tickets.loadMore")}
            </Button>
          )}
        </>
      )}

      {/* Announcements and notification settings are reached from here rather
          than from the tab bar. Both are places a customer visits occasionally,
          and support is the errand they are already on when they think of
          either. */}
      <SectionLabel>{translate("nav.preferences")}</SectionLabel>
      <nav className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
        <SideLink
          description={translate("nav.newsDescription")}
          href="/account/news"
          icon={<Megaphone aria-hidden className="size-4 text-muted-foreground" />}
          label={translate("nav.news")}
        />
        <SideLink
          description={translate("nav.preferencesDescription")}
          href="/account/preferences"
          icon={<Bell aria-hidden className="size-4 text-muted-foreground" />}
          label={translate("nav.preferences")}
        />
      </nav>
    </div>
  );
}

function SideLink({
  description,
  href,
  icon,
  label,
}: {
  description: string;
  href: string;
  icon: ReactNode;
  label: string;
}) {
  return (
    <Link
      className="flex items-center gap-3 px-4 py-3.5 transition-colors hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring focus-visible:-outline-offset-2"
      href={href}
    >
      {icon}
      <span className="min-w-0 flex-1">
        <span className="block font-semibold text-[15px]">{label}</span>
        <span className="mt-0.5 block font-mono text-[11px] text-subtle-foreground">
          {description}
        </span>
      </span>
      <ChevronRight aria-hidden className="size-4 shrink-0 text-subtle-foreground" />
    </Link>
  );
}
