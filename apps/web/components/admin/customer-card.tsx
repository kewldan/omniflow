"use client";

import type { PanelCustomerCard } from "@omniflow/api-client/generated";
import { Avatar, AvatarFallback, AvatarImage } from "@omniflow/ui/avatar";
import { Badge } from "@omniflow/ui/badge";
import { Skeleton } from "@omniflow/ui/skeleton";
import Link from "next/link";
import { useFormatter, useLocale, useNow, useTranslations } from "next-intl";
import useSWR from "swr";

import { useEnumLabel } from "@/components/admin/enum-label";
import { type ApiError, fetcher } from "@/lib/api";
import { formatMoney } from "@/lib/money";
import { usePermission } from "@/lib/session";

/** How many subscriptions the card lists before it says how many more. */
const SHOWN_SUBSCRIPTIONS = 4;

/**
 * One customer's summary, read when an operator rests on a customer chip.
 *
 * The card is mounted only once the hover card opens, so the request goes out
 * after the hover delay and never for a chip nobody points at. SWR keeps the
 * answer for five minutes, so moving back and forth across a table does not
 * ask again.
 */
export function useCustomerCard(id: string | null) {
  return useSWR<PanelCustomerCard, ApiError>(
    id ? `/v1/panel/customers/${encodeURIComponent(id)}/card` : null,
    fetcher,
    { dedupingInterval: 5 * 60_000, revalidateOnFocus: false, shouldRetryOnError: false },
  );
}

function initials(card: PanelCustomerCard): string {
  const source = card.name || card.username || card.id;
  return source
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase() ?? "")
    .join("");
}

function statusVariant(status: string): "success" | "danger" | "neutral" {
  if (status === "active") {
    return "success";
  }
  return status === "suspended" ? "danger" : "neutral";
}

export function CustomerCardBody({ id }: { id: string }) {
  const translate = useTranslations("admin.customerCard");
  const allowed = usePermission("customers.read");
  const { data, error } = useCustomerCard(allowed ? id : null);

  if (!allowed || error?.status === 403) {
    return <p className="text-muted-foreground text-sm">{translate("forbidden")}</p>;
  }
  if (error?.status === 404) {
    return <p className="text-muted-foreground text-sm">{translate("notFound")}</p>;
  }
  if (error) {
    return <p className="text-destructive text-sm">{translate("failed")}</p>;
  }
  if (!data) {
    return <CustomerCardSkeleton />;
  }
  return <CustomerCardView card={data} />;
}

export function CustomerCardSkeleton() {
  const translate = useTranslations("admin.customerCard");
  return (
    <div aria-busy="true" className="flex flex-col gap-3">
      <span className="sr-only">{translate("loading")}</span>
      <div className="flex items-center gap-3">
        <Skeleton className="size-10 rounded-full" />
        <div className="flex flex-1 flex-col gap-1.5">
          <Skeleton className="h-4 w-32" />
          <Skeleton className="h-3 w-24" />
        </div>
      </div>
      <Skeleton className="h-3 w-40" />
      <Skeleton className="h-12 w-full" />
    </div>
  );
}

function CustomerCardView({ card }: { card: PanelCustomerCard }) {
  const translate = useTranslations("admin.customerCard");
  const statuses = useTranslations("admin.customers.status");
  const format = useFormatter();
  const locale = useLocale();
  const now = useNow({ updateInterval: 60_000 });
  const enumLabel = useEnumLabel();

  const title = card.name || (card.username ? `@${card.username}` : card.id.slice(0, 8));
  const handle = card.name && card.username ? `@${card.username}` : null;
  const created = new Date(card.createdAt);
  const shown = card.subscriptions.slice(0, SHOWN_SUBSCRIPTIONS);
  const hidden = card.subscriptions.length - shown.length;

  return (
    <div className="flex flex-col gap-3 text-sm">
      <div className="flex items-center gap-3">
        <Avatar className="size-10">
          {card.avatar && (
            <AvatarImage alt="" src={`/v1/panel/customers/${encodeURIComponent(card.id)}/avatar`} />
          )}
          <AvatarFallback className="text-xs">{initials(card)}</AvatarFallback>
        </Avatar>
        <div className="flex min-w-0 flex-col leading-tight">
          <span className="truncate font-medium">{title}</span>
          {handle && <span className="truncate text-muted-foreground text-xs">{handle}</span>}
          {card.telegramId !== undefined && (
            <span className="font-mono text-[11px] text-muted-foreground">
              {translate("telegramId", { id: String(card.telegramId) })}
            </span>
          )}
        </div>
      </div>

      <div className="flex flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={statusVariant(card.status)}>{statuses(card.status)}</Badge>
          {card.status === "suspended" && (
            <span className="text-muted-foreground text-xs">
              {card.suspensionEndsAt
                ? translate("suspendedUntil", {
                    date: format.dateTime(new Date(card.suspensionEndsAt), "datetime"),
                  })
                : translate("suspendedIndefinitely")}
            </span>
          )}
        </div>
        <span className="text-muted-foreground text-xs">
          {translate("since", {
            ago: format.relativeTime(created, now),
            date: format.dateTime(created, "date"),
          })}
        </span>
      </div>

      <section className="flex flex-col gap-1">
        <h3 className="font-mono text-[10px] text-muted-foreground uppercase tracking-[0.12em]">
          {translate("subscriptions")}
        </h3>
        {shown.length === 0 ? (
          <span className="text-muted-foreground text-xs">{translate("noSubscriptions")}</span>
        ) : (
          <ul className="flex flex-col gap-1">
            {shown.map((subscription) => {
              const state =
                subscription.status === "active" && subscription.entitlementStatus
                  ? enumLabel("entitlementStatus", subscription.entitlementStatus)
                  : enumLabel("subscriptionStatus", subscription.status);
              const live =
                subscription.status === "active" && subscription.entitlementStatus === "active";
              return (
                <li className="flex items-baseline justify-between gap-2" key={subscription.id}>
                  <span className="flex min-w-0 flex-col leading-tight">
                    <span className="truncate">
                      {subscription.label || subscription.planCode || translate("unnamed")}
                    </span>
                    {subscription.label && subscription.planCode && (
                      <span className="truncate font-mono text-[11px] text-muted-foreground">
                        {subscription.planCode}
                      </span>
                    )}
                  </span>
                  <span className="flex shrink-0 flex-col items-end leading-tight">
                    <Badge variant={live ? "success" : "neutral"}>{state}</Badge>
                    {subscription.endsAt && (
                      <span className="text-[11px] text-muted-foreground">
                        {translate("endsAt", {
                          date: format.dateTime(new Date(subscription.endsAt), "date"),
                        })}
                      </span>
                    )}
                  </span>
                </li>
              );
            })}
            {hidden > 0 && (
              <li className="text-muted-foreground text-xs">
                {translate("more", { count: hidden })}
              </li>
            )}
          </ul>
        )}
      </section>

      <section className="flex flex-col gap-1">
        <h3 className="font-mono text-[10px] text-muted-foreground uppercase tracking-[0.12em]">
          {translate("spent")}
        </h3>
        {card.spent.length === 0 ? (
          <span className="text-muted-foreground text-xs">{translate("nothingSpent")}</span>
        ) : (
          <ul className="flex flex-col gap-0.5">
            {card.spent.map((line) => (
              <li className="flex items-baseline justify-between gap-2" key={line.currency}>
                <span className="font-medium tabular-nums">
                  {formatMoney(line.spentMinor, line.currency, locale)}
                </span>
                <span className="text-muted-foreground text-xs">
                  {translate("orders", { count: line.orderCount })}
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <div className="flex items-center justify-between gap-2 border-border border-t pt-2">
        <span className="truncate font-mono text-[10px] text-muted-foreground">{card.id}</span>
        <Link
          className="shrink-0 text-xs underline-offset-2 hover:underline"
          href={`/admin/customers/${card.id}`}
        >
          {translate("open")}
        </Link>
      </div>
    </div>
  );
}
