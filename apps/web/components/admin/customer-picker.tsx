"use client";

import { Button } from "@omniflow/ui/button";
import {
  Command,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@omniflow/ui/command";
import { cn } from "@omniflow/ui/lib/utils";
import { Popover, PopoverContent, PopoverTrigger } from "@omniflow/ui/popover";
import { ChevronsUpDown } from "lucide-react";
import { useTranslations } from "next-intl";
import { useEffect, useState } from "react";
import useSWR from "swr";

import { CustomerChip } from "@/components/admin/customer-chip";
import { type ApiError, fetcher } from "@/lib/api";

/**
 * Choosing a customer by typing, where a form used to ask for a pasted
 * identifier.
 *
 * It searches the way the command palette does, through the same route and
 * its limits: an ID or its first six characters, a Telegram ID, an exact
 * @username or e-mail, a Remnawave username. There is deliberately no
 * substring search over names — a picker that lists every "Ivan" is the
 * query that turns a support account into an export. Before anything is
 * typed it offers the customers this operator opened last, which is where
 * the other half of a merge usually is.
 */

const RECENT_KEY = "omniflow.admin.recentCustomers";
const RECENT_LIMIT = 8;

function readRecent(): string[] {
  try {
    const value: unknown = JSON.parse(window.localStorage.getItem(RECENT_KEY) ?? "[]");
    return Array.isArray(value) ? value.filter((id): id is string => typeof id === "string") : [];
  } catch {
    return [];
  }
}

/** Puts a customer at the front of this browser's recently opened list. */
export function rememberCustomer(id: string) {
  try {
    const list = [id, ...readRecent().filter((entry) => entry !== id)].slice(0, RECENT_LIMIT);
    window.localStorage.setItem(RECENT_KEY, JSON.stringify(list));
  } catch {
    // Storage switched off: the picker simply has nothing recent to offer.
  }
}

type SearchResponse = {
  groups: { group: string; items: { id: string }[] }[];
  hints: string[];
};

function useDebounced<Value>(value: Value, delay: number): Value {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delay);
    return () => clearTimeout(timer);
  }, [value, delay]);
  return debounced;
}

export function CustomerPicker({
  className,
  exclude,
  id,
  onChange,
  value,
}: {
  className?: string;
  /** A customer never offered, such as the one whose page this is. */
  exclude?: string;
  id?: string;
  onChange: (customerId: string) => void;
  value: string;
}) {
  const translate = useTranslations("admin.customerPicker");
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [recent, setRecent] = useState<string[]>([]);
  const term = useDebounced(query.trim(), 250);
  const searchable = term.length >= 2;
  const { data, isLoading } = useSWR<SearchResponse, ApiError>(
    open && searchable ? `/v1/panel/search?q=${encodeURIComponent(term)}` : null,
    fetcher,
    { keepPreviousData: true, revalidateOnFocus: false, shouldRetryOnError: false },
  );

  useEffect(() => {
    if (open) {
      setRecent(readRecent());
    }
  }, [open]);

  const found = searchable
    ? (data?.groups.find((group) => group.group === "customers")?.items ?? []).map((hit) => hit.id)
    : recent;
  const ids = found.filter((customerId) => customerId !== exclude);
  const pending = query.trim().length >= 2 && (isLoading || term !== query.trim());

  let empty = translate("hint");
  if (searchable) {
    if (pending) {
      empty = translate("searching");
    } else if (data?.hints.includes("telegram_handle_not_stored")) {
      empty = translate("handleNotFound");
    } else {
      empty = translate("nothing");
    }
  }

  function choose(customerId: string) {
    onChange(customerId);
    setOpen(false);
    setQuery("");
  }

  return (
    <Popover onOpenChange={setOpen} open={open}>
      <PopoverTrigger asChild>
        <Button
          className={cn(
            "h-auto min-h-9 w-full justify-between gap-2 py-1.5 font-normal",
            className,
          )}
          id={id}
          type="button"
          variant="outline"
        >
          {value ? (
            <CustomerChip id={value} link={false} />
          ) : (
            <span className="text-subtle-foreground">{translate("placeholder")}</span>
          )}
          <ChevronsUpDown aria-hidden className="size-4 shrink-0 text-subtle-foreground" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-[var(--radix-popover-trigger-width)] min-w-80 p-0">
        <Command shouldFilter={false}>
          <CommandInput onValueChange={setQuery} placeholder={translate("search")} value={query} />
          <CommandList>
            {ids.length === 0 ? (
              <p className="px-3 py-6 text-center text-muted-foreground text-sm">{empty}</p>
            ) : (
              <CommandGroup heading={translate(searchable ? "found" : "recent")}>
                {ids.map((customerId) => (
                  <CommandItem
                    key={customerId}
                    onSelect={() => choose(customerId)}
                    value={customerId}
                  >
                    <CustomerChip id={customerId} link={false} />
                  </CommandItem>
                ))}
              </CommandGroup>
            )}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
