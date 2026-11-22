"use client";

import { Info } from "lucide-react";
import { type ReactNode, useState } from "react";

import { cn } from "./lib/utils";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "./tooltip";

/**
 * A small ⓘ beside a short label that explains it in full.
 *
 * Labels stay one line so a row of fields lines up; the sentence that says
 * exactly what a filter or setting means goes here. It opens on hover and on
 * keyboard focus, and — since a touch screen has no hover — on a tap too. The
 * trigger is a real button with the explanation as its accessible name, so a
 * screen reader hears it without opening anything.
 */
export function InfoTip({
  children,
  className,
  label,
}: {
  /** The explanation. */
  children: ReactNode;
  className?: string;
  /** Spoken name of the trigger; the explanation itself when it is plain text. */
  label?: string;
}) {
  const [open, setOpen] = useState(false);
  return (
    <TooltipProvider delayDuration={150}>
      <Tooltip onOpenChange={setOpen} open={open}>
        <TooltipTrigger asChild>
          <button
            aria-label={label ?? (typeof children === "string" ? children : undefined)}
            className={cn(
              "inline-flex size-4 shrink-0 items-center justify-center rounded-full align-middle text-muted-foreground transition-colors hover:text-foreground",
              "focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2",
              className,
            )}
            onClick={(event) => {
              // A label's click would otherwise focus or toggle its control.
              event.preventDefault();
              setOpen((current) => !current);
            }}
            type="button"
          >
            <Info aria-hidden className="size-3.5" />
          </button>
        </TooltipTrigger>
        <TooltipContent className="max-w-64 text-pretty leading-snug">{children}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
