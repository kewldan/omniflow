"use client";

import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useCallback, useMemo } from "react";

/**
 * Keeps list filters and the cursor in the URL.
 *
 * Two things fall out of that. A view is bookmarkable and shareable, which is
 * how one operator hands a colleague exactly what they were looking at. And the
 * browser's back button steps through pages, because each page is a real
 * history entry rather than hidden component state.
 *
 * Cursor pagination is forward-only — the API hands out a cursor for the next
 * page and nothing else — so the cursors already visited are carried in the URL
 * as a stack. That is what makes a "previous" control possible at all.
 */
export type UrlFilters = {
  filters: Record<string, string> & { cursor: string };
  /** Cursors for the pages behind the current one, oldest first. */
  cursorStack: string[];
  hasPrevious: boolean;
  setFilter: (key: string, value: string) => void;
  reset: () => void;
  goNext: (cursor: string | undefined) => void;
  goPrevious: () => void;
};

const CURSOR_KEY = "cursor";
const STACK_KEY = "seen";

export function useUrlFilters(keys: string[]): UrlFilters {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();

  /*
   * Call sites pass a fresh array literal on every render, so the identity of
   * `keys` is never stable. Collapsing it to a string first gives the memo a
   * dependency that actually compares by content.
   */
  const keyList = keys.join(",");

  const filters = useMemo(() => {
    const values: Record<string, string> & { cursor: string } = { cursor: "" };
    for (const key of keyList ? keyList.split(",") : []) {
      values[key] = searchParams.get(key) ?? "";
    }
    values.cursor = searchParams.get(CURSOR_KEY) ?? "";
    return values;
  }, [keyList, searchParams]);

  const cursorStack = useMemo(() => {
    const raw = searchParams.get(STACK_KEY);
    return raw ? raw.split("~").filter(Boolean) : [];
  }, [searchParams]);

  const push = useCallback(
    (next: URLSearchParams, replace: boolean) => {
      const query = next.toString();
      const url = query ? `${pathname}?${query}` : pathname;
      if (replace) {
        router.replace(url, { scroll: false });
      } else {
        router.push(url, { scroll: false });
      }
    },
    [pathname, router],
  );

  const setFilter = useCallback(
    (key: string, value: string) => {
      const next = new URLSearchParams(searchParams.toString());
      if (value) {
        next.set(key, value);
      } else {
        next.delete(key);
      }
      // Changing a filter invalidates every cursor: they point into a result
      // set that no longer exists, so paging resets to the first page.
      next.delete(CURSOR_KEY);
      next.delete(STACK_KEY);
      push(next, true);
    },
    [push, searchParams],
  );

  const reset = useCallback(() => push(new URLSearchParams(), true), [push]);

  const goNext = useCallback(
    (cursor: string | undefined) => {
      if (!cursor) {
        return;
      }
      const next = new URLSearchParams(searchParams.toString());
      const stack = [...cursorStack, filters.cursor];
      next.set(CURSOR_KEY, cursor);
      next.set(STACK_KEY, stack.join("~"));
      push(next, false);
    },
    [cursorStack, filters.cursor, push, searchParams],
  );

  const goPrevious = useCallback(() => {
    const next = new URLSearchParams(searchParams.toString());
    const stack = [...cursorStack];
    const previous = stack.pop() ?? "";
    if (previous) {
      next.set(CURSOR_KEY, previous);
    } else {
      next.delete(CURSOR_KEY);
    }
    if (stack.length > 0) {
      next.set(STACK_KEY, stack.join("~"));
    } else {
      next.delete(STACK_KEY);
    }
    push(next, false);
  }, [cursorStack, push, searchParams]);

  return {
    cursorStack,
    filters,
    hasPrevious: cursorStack.length > 0,
    goNext,
    goPrevious,
    reset,
    setFilter,
  };
}
