"use client";

import { Input, Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { useTranslations } from "next-intl";
import { useId } from "react";

import { useBytes } from "@/lib/format";

import type { SupportLimits } from "./types";

/**
 * A text field that counts down to the same number the API refuses at.
 *
 * The limit is read from `/support/limits` rather than written here, because the
 * API counts characters against a column check and a second copy of that figure
 * in the panel would be a second copy that drifts. `maxLength` stops the typing
 * and the caption explains why, so the customer is never left wondering whether
 * the keyboard broke.
 *
 * The caption is announced politely rather than assertively: a count that
 * interrupted every keystroke would make the field unusable with a screen reader.
 */
export function CountedField({
  disabled,
  invalid,
  label,
  maxLength,
  multiline,
  onChange,
  placeholder,
  value,
}: {
  disabled?: boolean;
  invalid?: string;
  label: string;
  maxLength: number;
  multiline?: boolean;
  onChange: (value: string) => void;
  placeholder?: string;
  value: string;
}) {
  const translate = useTranslations("account.support");
  const fieldId = useId();
  const captionId = useId();
  const remaining = Math.max(0, maxLength - value.length);

  return (
    <div className="space-y-1.5">
      <Label htmlFor={fieldId}>{label}</Label>
      {multiline ? (
        <Textarea
          aria-describedby={captionId}
          aria-invalid={invalid ? true : undefined}
          className="min-h-32"
          disabled={disabled}
          id={fieldId}
          maxLength={maxLength}
          onChange={(event) => onChange(event.target.value)}
          placeholder={placeholder}
          value={value}
        />
      ) : (
        <Input
          aria-describedby={captionId}
          aria-invalid={invalid ? true : undefined}
          disabled={disabled}
          id={fieldId}
          maxLength={maxLength}
          onChange={(event) => onChange(event.target.value)}
          placeholder={placeholder}
          value={value}
        />
      )}
      {invalid ? (
        <p className="text-[11.5px] text-destructive leading-relaxed" id={captionId} role="alert">
          {invalid}
        </p>
      ) : (
        <p
          aria-live="polite"
          className="font-mono text-[10.5px] text-subtle-foreground"
          id={captionId}
        >
          {translate("new.remaining", { count: remaining })}
        </p>
      )}
    </div>
  );
}

/**
 * What this installation accepts, stated before an upload rather than after one.
 *
 * A customer who learns the size cap from a refusal has already spent the time it
 * took to send the file, and on a phone connection that is the whole cost of the
 * mistake. The figures come from `/support/limits`, so an installation that
 * configures a different cap or a narrower allowlist says so here without a
 * change to this component.
 */
export function AttachmentLimits({ limits }: { limits: SupportLimits }) {
  const translate = useTranslations("account.support");
  const formatBytes = useBytes();
  return (
    <p className="font-mono text-[11px] text-subtle-foreground leading-relaxed">
      {translate("attachments.hint", {
        size: formatBytes(limits.maxAttachmentBytes),
        types: limits.allowedMediaTypes.join(", "),
      })}
    </p>
  );
}
