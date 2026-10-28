"use client";

import { CommandGroup, CommandItem } from "@omniflow/ui/command";
import { Gift, Info, KeyRound, LifeBuoy, Loader2, Receipt, User } from "lucide-react";
import { useTranslations } from "next-intl";
import { type ComponentType, useEffect, useState } from "react";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";

type SearchGroupKey = "customers" | "orders" | "tickets" | "gifts" | "accessCodes";

type SearchHit = {
  id: string;
  matchedBy: string;
  status?: string;
  createdAt?: string;
  telegramId?: number;
  customerId?: string;
  state?: string;
  operation?: string;
  currency?: string;
  totalMinor?: number;
  provider?: string;
  subject?: string;
  lastMessageAt?: string;
  kind?: string;
  codeHint?: string;
  expiresAt?: string;
  batchReference?: string;
};

type SearchResponse = {
  groups: { group: SearchGroupKey; items: SearchHit[] }[];
  hints: string[];
};

const GROUP_ICONS: Record<SearchGroupKey, ComponentType<{ "aria-hidden"?: boolean }>> = {
  accessCodes: KeyRound,
  customers: User,
  gifts: Gift,
  orders: Receipt,
  tickets: LifeBuoy,
};

/** Where a hit opens. Each target is guarded by the same permission as its group. */
function hrefFor(group: SearchGroupKey, hit: SearchHit): string {
  switch (group) {
    case "customers":
      return `/admin/customers/${hit.id}`;
    case "orders":
      return `/admin/finance/${hit.id}`;
    case "tickets":
      return `/admin/support?ticketId=${hit.id}`;
    case "gifts":
      return hit.status ? `/admin/gifts?status=${encodeURIComponent(hit.status)}` : "/admin/gifts";
    case "accessCodes":
      return "/admin/catalog";
  }
}

/**
 * Debounces a value. The palette searches as the operator types, and a request
 * per keystroke would spend the per-operator rate limit on prefixes nobody
 * meant to search for.
 */
function useDebounced<Value>(value: Value, delay: number): Value {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delay);
    return () => clearTimeout(timer);
  }, [value, delay]);
  return debounced;
}

/**
 * Record hits for the command palette.
 *
 * Loaded on demand the first time the palette opens rather than shipped with
 * the shell: the shell is in every panel bundle, and record search is only
 * needed once somebody types. Groups are whatever the API returned, which is
 * only ever what the operator may read.
 */
export function RecordResults({
  onPendingChange,
  onSelect,
  search,
}: {
  onPendingChange: (pending: boolean) => void;
  onSelect: (href: string) => void;
  search: string;
}) {
  const translate = useTranslations("admin.commandMenu.search");
  const money = useMoney();
  const trimmed = search.trim();
  const term = useDebounced(trimmed, 250);
  const searchable = term.length >= 2;
  const { data, error, isLoading } = useSWR<SearchResponse, ApiError>(
    searchable ? `/v1/panel/search?q=${encodeURIComponent(term)}` : null,
    fetcher,
    { keepPreviousData: true, revalidateOnFocus: false, shouldRetryOnError: false },
  );
  const groups = searchable ? (data?.groups ?? []).filter((group) => group.items.length > 0) : [];
  const pending = trimmed.length >= 2 && (isLoading || term !== trimmed);

  useEffect(() => {
    onPendingChange(pending);
  }, [pending, onPendingChange]);

  function status(value: string | undefined): string {
    if (!value) {
      return "";
    }
    return translate.has(`statuses.${value}`) ? translate(`statuses.${value}`) : value;
  }

  function describe(group: SearchGroupKey, hit: SearchHit): { title: string; detail: string } {
    const shortId = hit.id.slice(0, 8);
    const parts: string[] = [];
    switch (group) {
      case "customers":
        if (hit.telegramId) {
          parts.push(translate("telegram", { id: String(hit.telegramId) }));
        }
        parts.push(status(hit.status));
        if (hit.createdAt) {
          parts.push(translate("since", { date: new Date(hit.createdAt) }));
        }
        return { detail: parts.join(" · "), title: translate("customer", { id: shortId }) };
      case "orders":
        parts.push(status(hit.state));
        if (hit.provider) {
          parts.push(hit.provider);
        }
        if (hit.createdAt) {
          parts.push(translate("created", { date: new Date(hit.createdAt) }));
        }
        return {
          detail: parts.join(" · "),
          title: translate("order", {
            amount: money(hit.totalMinor ?? 0, hit.currency ?? "USD"),
            id: shortId,
          }),
        };
      case "tickets":
        parts.push(status(hit.status));
        if (hit.lastMessageAt) {
          parts.push(translate("lastMessage", { date: new Date(hit.lastMessageAt) }));
        }
        return {
          detail: parts.join(" · "),
          title: hit.subject?.trim() || translate("ticket", { id: shortId }),
        };
      case "gifts":
        parts.push(status(hit.status));
        if (hit.expiresAt) {
          parts.push(translate("expires", { date: new Date(hit.expiresAt) }));
        }
        return {
          detail: parts.join(" · "),
          title: translate("gift", { hint: hit.codeHint ?? "" }),
        };
      case "accessCodes":
        parts.push(status(hit.status));
        if (hit.batchReference) {
          parts.push(translate("batch", { reference: hit.batchReference }));
        }
        return {
          detail: parts.join(" · "),
          title: translate("accessCode", { hint: hit.codeHint ?? "" }),
        };
    }
  }

  return (
    <>
      {pending && groups.length === 0 ? (
        <div
          aria-live="polite"
          className="flex items-center justify-center gap-2 py-6 text-muted-foreground text-sm"
          role="status"
        >
          <Loader2 aria-hidden className="size-4 animate-spin" />
          {translate("searching")}
        </div>
      ) : null}
      {searchable && error ? (
        <p className="px-4 py-2 text-destructive text-xs" role="status">
          {error.code === "rate_limited" ? translate("rateLimited") : translate("failed")}
        </p>
      ) : null}
      {searchable && data?.hints.includes("telegram_handle_not_stored") ? (
        <p className="flex items-start gap-2 px-4 py-2 text-muted-foreground text-xs" role="status">
          <Info aria-hidden className="mt-0.5 size-3.5 shrink-0" />
          {translate("handleNotStored")}
        </p>
      ) : null}

      {groups.map((group) => {
        const Icon = GROUP_ICONS[group.group];
        return (
          <CommandGroup heading={translate(`groups.${group.group}`)} key={group.group}>
            {group.items.map((hit) => {
              const { detail, title } = describe(group.group, hit);
              return (
                <CommandItem
                  // The API already decided these match. Carrying the live
                  // input as a keyword keeps the palette's own filter from
                  // hiding a hit whose visible text does not contain it — an
                  // email match shows no email, by design.
                  keywords={[search]}
                  key={`${group.group}-${hit.id}`}
                  onSelect={() => onSelect(hrefFor(group.group, hit))}
                  value={`record ${group.group} ${hit.id}`}
                >
                  <Icon aria-hidden />
                  <div className="flex min-w-0 flex-col">
                    <span className="truncate">{title}</span>
                    <span className="truncate text-muted-foreground text-xs">
                      {[detail, translate(`matchedBy.${hit.matchedBy}`)]
                        .filter(Boolean)
                        .join(" · ")}
                    </span>
                  </div>
                </CommandItem>
              );
            })}
          </CommandGroup>
        );
      })}
    </>
  );
}
