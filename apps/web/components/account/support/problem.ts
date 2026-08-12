"use client";

import { useTranslations } from "next-intl";
import { useCallback } from "react";

import type { ApiError } from "@/lib/api";

/**
 * Turns a problem response into a sentence that names the next step.
 *
 * The API's own `detail` is written in English by a Go package that has no
 * locale, so it is never what the customer is shown. What is shown is keyed off
 * `code`, which the transport derives from the problem type URI and which is the
 * only part of a problem document this panel treats as a contract.
 *
 * An unrecognised code falls back to one generic line rather than to the raw
 * detail: a server-authored string rendered into the panel would arrive
 * untranslated and, worse, unreviewed.
 */
export function useProblemMessage(): (error: unknown) => string {
  const translate = useTranslations("account.support");
  return useCallback(
    (error: unknown) => {
      const code = (error as ApiError | undefined)?.code ?? "";
      const key = `problem.${code}`;
      return translate.has(key) ? translate(key) : translate("problem.unknown");
    },
    [translate],
  );
}
