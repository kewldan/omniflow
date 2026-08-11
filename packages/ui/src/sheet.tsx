"use client";

import * as SheetPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

export const Sheet = SheetPrimitive.Root;
export const SheetTrigger = SheetPrimitive.Trigger;
export const SheetClose = SheetPrimitive.Close;

export type SheetSide = "bottom" | "left" | "right" | "top";

const sideClasses: Record<SheetSide, string> = {
  bottom:
    "inset-x-0 bottom-0 h-auto max-h-[85vh] rounded-t-xl border-t data-[state=closed]:slide-out-to-bottom data-[state=open]:slide-in-from-bottom",
  left: "inset-y-0 left-0 h-full w-3/4 border-r sm:max-w-sm data-[state=closed]:slide-out-to-left data-[state=open]:slide-in-from-left",
  right:
    "inset-y-0 right-0 h-full w-3/4 border-l sm:max-w-sm data-[state=closed]:slide-out-to-right data-[state=open]:slide-in-from-right",
  top: "inset-x-0 top-0 h-auto rounded-b-xl border-b data-[state=closed]:slide-out-to-top data-[state=open]:slide-in-from-top",
};

export type SheetContentProps = ComponentProps<typeof SheetPrimitive.Content> & {
  side?: SheetSide;
};

export function SheetContent({ children, className, side = "right", ...props }: SheetContentProps) {
  return (
    <SheetPrimitive.Portal>
      <SheetPrimitive.Overlay
        className={cn(
          "fixed inset-0 z-50 bg-black/55 backdrop-blur-[2px]",
          "data-[state=closed]:animate-out data-[state=closed]:fade-out-0",
          "data-[state=open]:animate-in data-[state=open]:fade-in-0",
        )}
      />
      <SheetPrimitive.Content
        className={cn(
          "fixed z-50 flex flex-col gap-4 border-border bg-card p-5 shadow-[var(--glass-shadow)]",
          "transition ease-[var(--ease-emphasis)]",
          "data-[state=closed]:animate-out data-[state=closed]:duration-200",
          "data-[state=open]:animate-in data-[state=open]:duration-300",
          sideClasses[side],
          className,
        )}
        data-slot="sheet-content"
        {...props}
      >
        {children}
        <SheetPrimitive.Close
          className={cn(
            "absolute top-4 right-4 rounded-sm p-1 text-muted-foreground opacity-70 transition-opacity",
            "hover:opacity-100 focus-visible:ring-[3px] focus-visible:ring-ring/40 focus-visible:outline-none",
          )}
        >
          <X className="size-4" />
          <span className="sr-only">Close</span>
        </SheetPrimitive.Close>
      </SheetPrimitive.Content>
    </SheetPrimitive.Portal>
  );
}

export function SheetHeader({ className, ...props }: ComponentProps<"div">) {
  return (
    <div className={cn("flex flex-col gap-1.5", className)} data-slot="sheet-header" {...props} />
  );
}

export function SheetFooter({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cn("mt-auto flex flex-col gap-2", className)}
      data-slot="sheet-footer"
      {...props}
    />
  );
}

export function SheetTitle({ className, ...props }: ComponentProps<typeof SheetPrimitive.Title>) {
  return (
    <SheetPrimitive.Title
      className={cn("font-semibold text-[17px] tracking-tight", className)}
      data-slot="sheet-title"
      {...props}
    />
  );
}

export function SheetDescription({
  className,
  ...props
}: ComponentProps<typeof SheetPrimitive.Description>) {
  return (
    <SheetPrimitive.Description
      className={cn("text-muted-foreground text-sm", className)}
      data-slot="sheet-description"
      {...props}
    />
  );
}
