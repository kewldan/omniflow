"use client";

import { useTranslations } from "next-intl";
import { useCallback } from "react";

import type { ApiError } from "@/lib/api";

/**
 * The problem codes these four routes can answer with.
 *
 * Every one of them has copy in the catalogue that names a next step, because a
 * refusal a customer cannot act on is the same as a broken button. The list is
 * closed on purpose: a code that appears here without copy would fall through to
 * the server's English detail string, which is written for an operator reading a
 * log rather than for the person holding the phone.
 */
const KNOWN_CODES = new Set([
  "confirmation_required",
  "contact_unavailable",
  "contacts_unavailable",
  "deletion_pending",
  "invalid_input",
  "no_deletion_pending",
  "not_found",
  "rate_limited",
  "reauthentication_required",
]);

/**
 * Turns a rejected request into a sentence the customer can act on.
 *
 * `contact_unavailable` deserves the note. The server refuses to say whether the
 * address belongs to another account, and this copy keeps that promise: it says
 * the address cannot be added here and points at support, where a person can
 * establish who is asking before anything is revealed. Wording it as "already in
 * use" would undo the whole protection and turn the panel into a way of testing
 * whether somebody has an account.
 */
export function useProblemMessage(): (error: unknown) => string {
  const translate = useTranslations("account.account");
  return useCallback(
    (error: unknown) => {
      const problem = error as ApiError | undefined;
      const code = problem?.code ?? "";
      if (KNOWN_CODES.has(code)) {
        return translate(`errors.${code}`);
      }
      // A 5xx with no recognised code is still worth a sentence of its own: the
      // customer did nothing wrong and retrying is the right advice.
      return translate("errors.unknown");
    },
    [translate],
  );
}
