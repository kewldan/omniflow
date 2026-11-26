"use client";

import { Button } from "@omniflow/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@omniflow/ui/dialog";
import { Textarea } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Radio } from "@omniflow/ui/radio";
import { useTranslations } from "next-intl";
import { type ReactNode, useId, useState } from "react";

/** A ready-made reason: its text is what the audit log records. */
export type ReasonPreset = string;

const OTHER = "__other";

/**
 * How the panel asks why before an operator action is applied.
 *
 * The action opens this dialog instead of reading a reason field that sits on
 * the page: the operator picks one of the ready reasons for this action, or
 * writes their own under "Another reason". The chosen text is what the API
 * records in the audit log, so it is phrased for a reviewer reading it later.
 * Fields specific to the action (an amount, a ban length) go in `children`.
 */
export function ReasonDialog({
  open,
  onOpenChange,
  title,
  description,
  presets,
  confirmLabel,
  destructive = false,
  pending = false,
  error,
  disabled = false,
  children,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: ReactNode;
  presets: ReasonPreset[];
  confirmLabel: string;
  destructive?: boolean;
  pending?: boolean;
  error?: string | null;
  /** Holds the confirm button back while the action's own fields are invalid. */
  disabled?: boolean;
  children?: ReactNode;
  onConfirm: (reason: string) => void | Promise<void>;
}) {
  return (
    <Dialog onOpenChange={onOpenChange} open={open}>
      {open && (
        <ReasonForm
          confirmLabel={confirmLabel}
          description={description}
          destructive={destructive}
          disabled={disabled}
          error={error}
          onConfirm={onConfirm}
          pending={pending}
          presets={presets}
          title={title}
        >
          {children}
        </ReasonForm>
      )}
    </Dialog>
  );
}

function ReasonForm({
  title,
  description,
  presets,
  confirmLabel,
  destructive,
  pending,
  error,
  disabled,
  children,
  onConfirm,
}: {
  title: string;
  description?: ReactNode;
  presets: ReasonPreset[];
  confirmLabel: string;
  destructive: boolean;
  pending: boolean;
  error?: string | null;
  disabled: boolean;
  children?: ReactNode;
  onConfirm: (reason: string) => void | Promise<void>;
}) {
  const translate = useTranslations("admin.reasonDialog");
  const name = useId();
  const otherId = useId();
  const [choice, setChoice] = useState<string>(presets.length > 0 ? "0" : OTHER);
  const [other, setOther] = useState("");
  const reason = choice === OTHER ? other.trim() : (presets[Number(choice)] ?? "");

  return (
    <DialogContent className="sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>{title}</DialogTitle>
        {description && <DialogDescription>{description}</DialogDescription>}
      </DialogHeader>

      <form
        className="flex flex-col gap-4"
        onSubmit={(event) => {
          event.preventDefault();
          if (reason !== "" && !disabled && !pending) {
            void onConfirm(reason);
          }
        }}
      >
        {children}

        <fieldset className="flex flex-col gap-1.5">
          <legend className="mb-1.5 font-medium text-sm">{translate("reason")}</legend>
          {presets.map((preset, index) => {
            const id = `${name}-${index}`;
            return (
              <Label
                className="flex cursor-pointer items-center gap-2.5 rounded-md border px-3 py-2 font-normal has-[:checked]:border-primary has-[:checked]:bg-primary/5"
                htmlFor={id}
                key={preset}
              >
                <Radio
                  checked={choice === String(index)}
                  id={id}
                  name={name}
                  onChange={() => setChoice(String(index))}
                />
                {preset}
              </Label>
            );
          })}
          <Label
            className="flex cursor-pointer items-center gap-2.5 rounded-md border px-3 py-2 font-normal has-[:checked]:border-primary has-[:checked]:bg-primary/5"
            htmlFor={otherId}
          >
            <Radio
              checked={choice === OTHER}
              id={otherId}
              name={name}
              onChange={() => setChoice(OTHER)}
            />
            {translate("other")}
          </Label>
          {choice === OTHER && (
            <Textarea
              aria-label={translate("other")}
              autoFocus
              maxLength={400}
              onChange={(event) => setOther(event.target.value)}
              placeholder={translate("otherPlaceholder")}
              rows={2}
              value={other}
            />
          )}
        </fieldset>

        {error && (
          <p className="text-danger-foreground text-xs" role="alert">
            {error}
          </p>
        )}

        <DialogFooter>
          <Button
            disabled={pending || disabled || reason === ""}
            type="submit"
            variant={destructive ? "destructive" : "default"}
          >
            {confirmLabel}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  );
}
