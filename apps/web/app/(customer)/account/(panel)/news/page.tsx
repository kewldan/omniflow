"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { toast } from "@omniflow/ui/toast";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWRInfinite from "swr/infinite";
import { useSupportProblem } from "@/components/account/problems";
import { AccountNotice, ListSkeleton } from "@/components/account/state";
import { SupportAlerts } from "@/components/account/support/browser-notifications";
import {
  NEWS_KEY,
  type NewsItem,
  type NewsPage as NewsPagePayload,
} from "@/components/account/support/types";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, apiFetch, fetcher, toQuery } from "@/lib/api";

function newsPageKey(index: number, previous: NewsPagePayload | null) {
  if (previous && !previous.nextCursor) {
    return null;
  }
  if (index === 0 || !previous?.nextCursor) {
    return NEWS_KEY;
  }
  return `/v1/account/news${toQuery({ cursor: previous.nextCursor, limit: 20 })}`;
}

/**
 * The announcement inbox.
 *
 * No locale is sent with the request. The server resolves which language a
 * customer reads announcements in — the panel's switch is one input to that, but
 * so is the bot preference and the account's own locale, and forcing this page's
 * locale would make the same customer read the same announcement in two
 * languages depending on which surface they opened. What the page does instead is
 * say which language it was given when that is not the one this page is in.
 */
export default function NewsInboxPage() {
  const translate = useTranslations("account.support");
  const locale = useLocale();
  const { data, error, isLoading, isValidating, mutate, setSize, size } = useSWRInfinite<
    NewsPagePayload,
    ApiError
  >(newsPageKey, fetcher);

  const pages = data ?? [];
  const first = pages[0];
  const items = [...(first?.featured ?? []), ...pages.flatMap((page) => page.items)];
  const hasMore = Boolean(pages.at(-1)?.nextCursor);
  const loadingMore = isValidating && pages.length < size;

  return (
    <div className="animate-step-in space-y-4">
      <SupportAlerts />

      <header className="space-y-1">
        <h1 className="font-semibold text-[19px] tracking-[-0.02em]">{translate("news.title")}</h1>
        <p className="text-[12.5px] text-muted-foreground leading-relaxed">
          {translate("news.subtitle")}
        </p>
        {first && (
          <p className="text-[12.5px] text-subtle-foreground" role="status">
            {translate("news.unread", { count: first.unreadCount })}
          </p>
        )}
      </header>

      {first && first.locale !== locale && (
        <p
          className="rounded-lg border border-border bg-card px-4 py-3 text-[12.5px] text-muted-foreground leading-relaxed"
          role="status"
        >
          {translate("news.localeNotice", { language: translate(`news.language.${first.locale}`) })}
        </p>
      )}

      {isLoading ? (
        <ListSkeleton />
      ) : error ? (
        <AccountNotice
          action={<Button onClick={() => mutate()}>{translate("actions.retry")}</Button>}
          description={translate("news.errorDescription")}
          title={translate("news.error")}
          variant={error.status === 503 ? "offline" : "danger"}
        />
      ) : items.length === 0 ? (
        <AccountNotice
          description={translate("news.emptyDescription")}
          title={translate("news.empty")}
        />
      ) : (
        <>
          <ol className="space-y-3">
            {items.map((item) => (
              <li key={item.id}>
                <NewsCard item={item} onRead={mutate} />
              </li>
            ))}
          </ol>
          {hasMore && (
            <Button
              className="w-full"
              disabled={loadingMore}
              onClick={() => setSize(size + 1)}
              size="lg"
              variant="outline"
            >
              {loadingMore ? translate("tickets.loading") : translate("news.loadMore")}
            </Button>
          )}
        </>
      )}
    </div>
  );
}

/**
 * One announcement.
 *
 * Reading is an explicit act here rather than something the page infers from a
 * scroll position. The same `news_reads` row backs the Telegram inbox, so a post
 * marked read here stops being unread there — which is a claim worth only making
 * when the customer actually said so.
 *
 * A promotional post is labelled as one. The customer consented to receive it and
 * is entitled to see which of these is a service notice and which is an offer,
 * without having to infer it from the wording.
 */
function NewsCard({ item, onRead }: { item: NewsItem; onRead: () => Promise<unknown> }) {
  const translate = useTranslations("account.support");
  const describeProblem = useSupportProblem();
  const [busy, setBusy] = useState(false);

  async function markRead() {
    setBusy(true);
    try {
      await apiFetch(`/v1/account/news/${item.id}/read`, { method: "POST" });
      await onRead();
      toast.success(translate("news.marked"));
    } catch (readError) {
      toast.error(describeProblem(readError));
    } finally {
      setBusy(false);
    }
  }

  return (
    <article className="space-y-2.5 rounded-lg border border-border bg-card p-4">
      {item.coverUrl && (
        // An operator's https image of unknown size: a plain img with a fixed
        // aspect box, lazily loaded and sent without a referrer.
        // biome-ignore lint/performance/noImgElement: operator-hosted image on an unknown host
        <img
          alt=""
          className="aspect-[2/1] w-full rounded-md object-cover"
          loading="lazy"
          referrerPolicy="no-referrer"
          src={item.coverUrl}
        />
      )}
      <div className="flex flex-wrap items-center gap-2">
        {item.featured && <Badge variant="solid">{translate("news.featured")}</Badge>}
        <Badge variant="outline">
          <span
            aria-hidden
            className="size-2 rounded-full"
            style={{ backgroundColor: item.categoryColor }}
          />
          {item.categoryName}
        </Badge>
        {item.class === "marketing" && (
          <Badge variant="outline">{translate("news.class.marketing")}</Badge>
        )}
        {!item.read && (
          <span className="font-semibold text-[13px] text-accent-foreground">
            {translate("news.new")}
          </span>
        )}
        <time
          className="ml-auto shrink-0 text-[12px] text-subtle-foreground"
          dateTime={item.publishedAt}
        >
          <Timestamp value={item.publishedAt} />
        </time>
      </div>

      <h2 className="font-semibold text-[15.5px] leading-snug tracking-[-0.01em]">{item.title}</h2>
      {/* The body is operator-authored plain text. Their paragraph breaks are
          preserved and nothing in it is interpreted as markup. */}
      <p className="whitespace-pre-wrap text-[13.5px] leading-relaxed">{item.body}</p>
      {item.tags.length > 0 && (
        <ul aria-label={translate("news.tags")} className="flex flex-wrap gap-1.5">
          {item.tags.map((tag) => (
            <li className="text-[12.5px] text-muted-foreground" key={tag}>
              #{tag}
            </li>
          ))}
        </ul>
      )}

      {item.read ? (
        <p className="text-[12px] text-subtle-foreground">{translate("news.read")}</p>
      ) : (
        <Button disabled={busy} onClick={markRead} size="sm" variant="outline">
          {busy ? translate("news.marking") : translate("news.markRead")}
        </Button>
      )}
    </article>
  );
}
