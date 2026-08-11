"use client";

import { type ReactNode, useId, useState } from "react";

import { Button } from "./button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./dialog";
import { Input } from "./input";
import { Label } from "./label";

export type ConfirmDialogProps = {
  cancelLabel: string;
  confirmLabel: string;
  description?: ReactNode;
  /**
   * When set, the confirm button stays disabled until the operator types this
   * exact value. Reserved for actions that cannot be undone.
   */
  confirmationPhrase?: string;
  confirmationPrompt?: string;
  destructive?: boolean;
  onConfirm: () => void;
  onOpenChange: (open: boolean) => void;
  open: boolean;
  pending?: boolean;
  title: ReactNode;
};

/**
 * Confirmation gate for consequential actions. Typing the target's own name is
 * the safeguard for irreversible operations, so a mis-aimed click on a busy row
 * cannot destroy the wrong record.
 */
export function ConfirmDialog({
  cancelLabel,
  confirmLabel,
  confirmationPhrase,
  confirmationPrompt,
  description,
  destructive,
  onConfirm,
  onOpenChange,
  open,
  pending,
  title,
}: ConfirmDialogProps) {
  const [typed, setTyped] = useState("");
  const inputId = useId();
  const unlocked = !confirmationPhrase || typed.trim() === confirmationPhrase;

  function handleOpenChange(next: boolean) {
    if (!next) {
      setTyped("");
    }
    onOpenChange(next);
  }

  return (
    <Dialog onOpenChange={handleOpenChange} open={open}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        {confirmationPhrase && (
          <div className="flex flex-col gap-2">
            <Label htmlFor={inputId}>{confirmationPrompt}</Label>
            <Input
              autoComplete="off"
              id={inputId}
              onChange={(event) => setTyped(event.target.value)}
              value={typed}
            />
          </div>
        )}
        <DialogFooter>
          <Button onClick={() => handleOpenChange(false)} type="button" variant="outline">
            {cancelLabel}
          </Button>
          <Button
            disabled={!unlocked || pending}
            onClick={onConfirm}
            type="button"
            variant={destructive ? "destructive" : "default"}
          >
            {confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
