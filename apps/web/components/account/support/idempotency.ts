"use client";

import { useCallback, useRef } from "react";

/**
 * A key that survives a resubmission of the same text but not an edit of it.
 *
 * `Idempotency-Key` is optional on the customer routes, and what it buys is
 * exactly one thing: a double-tapped Send, or a form the browser replayed, must
 * reach the message that already exists rather than posting a second one. So the
 * key is derived from the content being sent — resubmitting the same words
 * carries the same key and is deduplicated, while editing the words is a
 * genuinely different message and earns a new one. A key fixed at mount would
 * make the second, corrected attempt collide with the first.
 *
 * `crypto.randomUUID` needs a secure context. Where it is missing — a plain-HTTP
 * deployment on a LAN, say — the caller sends no header at all, because a
 * predictable key is worse than none: two customers could collide on it.
 */
export function useIdempotencyKey(): (content: string) => string {
  const held = useRef({ content: "", key: "" });
  return useCallback((content: string) => {
    if (held.current.key && held.current.content === content) {
      return held.current.key;
    }
    const key =
      typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
        ? crypto.randomUUID()
        : "";
    held.current = { content, key };
    return key;
  }, []);
}

/** Builds the request headers, omitting the key when none could be generated. */
export function idempotentHeaders(key: string): HeadersInit | undefined {
  return key ? { "Idempotency-Key": key } : undefined;
}
