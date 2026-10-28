"use client";

import { Label } from "@omniflow/ui/label";
import { useTranslations } from "next-intl";
import { type ReactNode, useId } from "react";

import { decodeIssue } from "@/lib/form-schema";

/** What a field's control needs to be labelled, described, and marked invalid. */
export type FieldControlProps = {
  id: string;
  "aria-invalid": boolean;
  "aria-describedby"?: string;
};

/**
 * One labelled form field: the label, the control, a hint, and the error.
 *
 * The control is a render prop so that whatever it is — an `Input`, a
 * `MoneyInput`, a `Select` trigger — receives the same id, `aria-invalid`, and
 * `aria-describedby`, and a screen reader announces the hint and the error with
 * the field rather than leaving them as text somewhere below it.
 */
export function FormField({
  children,
  error,
  hint,
  label,
}: {
  children: (control: FieldControlProps) => ReactNode;
  /** An issue message from `lib/form-schema.ts`, usually `formState.errors.x?.message`. */
  error?: string;
  hint?: ReactNode;
  label: ReactNode;
}) {
  const id = useId();
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  const describedBy = [hint ? hintId : null, error ? errorId : null].filter(Boolean).join(" ");

  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children({
        "aria-describedby": describedBy || undefined,
        "aria-invalid": Boolean(error),
        id,
      })}
      {hint && (
        <span className="text-muted-foreground text-xs" id={hintId}>
          {hint}
        </span>
      )}
      <FieldMessage id={errorId} message={error} />
    </div>
  );
}

/**
 * Renders an issue message produced by `lib/form-schema.ts` in the reader's
 * language. A message that is not one of ours — a server's own sentence, say —
 * is shown as it is rather than dropped.
 */
export function FieldMessage({ id, message }: { id?: string; message?: string }) {
  const translate = useTranslations("common.form.errors");
  if (!message) {
    return null;
  }
  const { key, values } = decodeIssue(message);
  return (
    <span className="text-danger-foreground text-xs" id={id} role="alert">
      {translate.has(key) ? translate(key, values) : message}
    </span>
  );
}
