"use client";

import { useCallback, useRef } from "react";

import { ApiError } from "@/lib/api";

/**
 * One idempotency key per submission, held across every retry of it.
 *
 * The API requires an `Idempotency-Key` on confirming a checkout, starting a
 * payment, topping up a wallet, replying to a support ticket, and previewing a
 * bulk action. Those are the requests where sending two must not produce two
 * outcomes — two orders, two answers to the same customer, two bulk previews.
 * The key is what makes a repeat resolve to the record that already exists.
 *
 * It lives here rather than beside the checkout because both panels need it. It
 * did live beside the checkout, and the operator panel consequently sent no key
 * at all: replying to a ticket and previewing a bulk action answered 422 every
 * time, on every installation, because the button that would have carried the
 * key was written where the hook was not in view.
 *
 * The rule this encodes is when the key changes, which is the part that is easy
 * to get wrong in both directions:
 *
 *   - A double tap, or a retry after a connection that dropped before an answer
 *     arrived, is the SAME submission. It reuses the key, so the second request
 *     resolves to the first one's order instead of charging the customer twice.
 *   - An attempt the server explicitly refused — a 4xx or 5xx it actually
 *     answered with — is finished. Pressing the button again is a NEW submission
 *     and gets a new key, because reusing the old one against a changed checkout
 *     would either replay a stale intent or be rejected for reusing a key with
 *     different parameters.
 *
 * A component holds one of these per action it can submit, so a checkout's
 * "confirm" and an order's "pay" never share a key.
 */
export type Submission = {
  /**
   * The key for the attempt about to be made: minted on the first call and
   * returned unchanged until the submission is retired.
   */
  begin: () => string;
  /** Ends the current submission so the next one starts a fresh key. */
  retire: () => void;
  /**
   * Retires the submission only when the server answered, which is what
   * distinguishes "refused, start over" from "never arrived, retry as-is".
   */
  settle: (error?: unknown) => void;
};

/**
 * Generates a key.
 *
 * `crypto.randomUUID` is the right source and is present in every browser these
 * panels support over HTTPS, but it is undefined on an insecure origin — which
 * is exactly what a self-hosted installation looks like the first time an
 * operator opens it over plain HTTP on a LAN. Falling back to random bytes keeps
 * a purchase possible there rather than throwing inside the confirm handler.
 */
function newKey(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  const bytes = new Uint8Array(20);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

/** Holds one submission's idempotency key for as long as it is being retried. */
export function useSubmission(): Submission {
  const key = useRef<string | null>(null);

  const begin = useCallback(() => {
    if (key.current === null) {
      key.current = newKey();
    }
    return key.current;
  }, []);

  const retire = useCallback(() => {
    key.current = null;
  }, []);

  const settle = useCallback((error?: unknown) => {
    // No error, or an error the server produced: either way this submission is
    // over. A transport failure leaves the key in place, because the request may
    // well have been processed and the retry has to be able to find it.
    if (error === undefined || error instanceof ApiError) {
      key.current = null;
    }
  }, []);

  return { begin, retire, settle };
}
