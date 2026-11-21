"use client";

import { Avatar, AvatarFallback, AvatarImage } from "@omniflow/ui/avatar";
import { cn } from "@omniflow/ui/lib/utils";
import Link from "next/link";
import useSWR from "swr";

import { fetcher } from "@/lib/api";

/** How the panel names a customer: what `GET /v1/panel/customers/labels` answers. */
export type CustomerLabel = {
  id: string;
  name?: string;
  username?: string;
  telegramId?: number;
  avatar: boolean;
};

/** The most identifiers one labels request carries; the API's own bound. */
const BATCH = 200;

type Waiter = { resolve: (label: CustomerLabel | null) => void; reject: (error: unknown) => void };

/**
 * Label lookups are batched: every chip on a screen asks in the same tick, and
 * one request answers them all. A table of fifty orders is one round trip,
 * not fifty.
 */
const waiting = new Map<string, Waiter[]>();
let scheduled = false;

function flush() {
  scheduled = false;
  const batch = new Map(waiting);
  waiting.clear();
  const ids = [...batch.keys()];
  for (let start = 0; start < ids.length; start += BATCH) {
    const chunk = ids.slice(start, start + BATCH);
    fetcher<{ items: CustomerLabel[] }>(`/v1/panel/customers/labels?ids=${chunk.join(",")}`)
      .then((data) => {
        const found = new Map(data.items.map((item) => [item.id, item]));
        for (const id of chunk) {
          for (const waiter of batch.get(id) ?? []) {
            waiter.resolve(found.get(id) ?? null);
          }
        }
      })
      .catch((error: unknown) => {
        for (const id of chunk) {
          for (const waiter of batch.get(id) ?? []) {
            waiter.reject(error);
          }
        }
      });
  }
}

function loadLabel(id: string): Promise<CustomerLabel | null> {
  return new Promise((resolve, reject) => {
    const list = waiting.get(id) ?? [];
    list.push({ reject, resolve });
    waiting.set(id, list);
    if (!scheduled) {
      scheduled = true;
      setTimeout(flush, 0);
    }
  });
}

/** One customer's label, shared through SWR's cache across every chip. */
export function useCustomerLabel(id: string | null | undefined) {
  return useSWR(id ? ["customer-label", id] : null, () => loadLabel(id as string), {
    dedupingInterval: 5 * 60_000,
    revalidateOnFocus: false,
  });
}

/** The words a chip shows: the name, else the @username, else nothing. */
export function customerTitle(label: CustomerLabel | null | undefined): string | null {
  if (label?.name) {
    return label.name;
  }
  return label?.username ? `@${label.username}` : null;
}

function initials(title: string | null, id: string): string {
  if (!title) {
    return id.slice(0, 2).toUpperCase();
  }
  return title
    .replace(/^@/, "")
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase() ?? "")
    .join("");
}

/**
 * A customer wherever the panel lists one: their Telegram picture, name, and
 * @username, linking to their card. A customer without a Telegram name keeps
 * the short identifier, which is what every row used to show.
 */
export function CustomerChip({
  id,
  link = true,
  className,
}: {
  id: string;
  /** Off where the row itself already links to the customer. */
  link?: boolean;
  className?: string;
}) {
  const { data: label } = useCustomerLabel(id);
  const title = customerTitle(label);
  const secondary = label?.name && label.username ? `@${label.username}` : null;
  const body = (
    <span className={cn("inline-flex min-w-0 items-center gap-2", className)} title={id}>
      <Avatar className="size-6">
        {label?.avatar && (
          <AvatarImage alt="" src={`/v1/panel/customers/${encodeURIComponent(id)}/avatar`} />
        )}
        <AvatarFallback className="text-[10px]">{initials(title, id)}</AvatarFallback>
      </Avatar>
      <span className="flex min-w-0 flex-col leading-tight">
        {title ? (
          <span className="truncate text-[13px]">{title}</span>
        ) : (
          <span className="font-mono text-[11px]">{id.slice(0, 8)}</span>
        )}
        {secondary && (
          <span className="truncate text-[11px] text-muted-foreground">{secondary}</span>
        )}
      </span>
    </span>
  );
  if (!link) {
    return body;
  }
  return (
    <Link className="min-w-0 underline-offset-2 hover:underline" href={`/admin/customers/${id}`}>
      {body}
    </Link>
  );
}

/**
 * Labels for a list of customers at once, keyed by identifier: for a chart or
 * a table that needs every name before it draws. It rides the same batch and
 * cache as the chips.
 */
export function useCustomerLabels(ids: (string | null | undefined)[]) {
  const wanted = [...new Set(ids.filter((id): id is string => Boolean(id)))].sort();
  return useSWR(
    wanted.length > 0 ? ["customer-labels", wanted.join(",")] : null,
    async () => {
      const labels = await Promise.all(wanted.map((id) => loadLabel(id)));
      return new Map(
        labels
          .filter((label): label is CustomerLabel => label !== null)
          .map((label) => [label.id, label]),
      );
    },
    { dedupingInterval: 5 * 60_000, revalidateOnFocus: false },
  );
}
