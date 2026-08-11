"use client";

import * as DropdownMenuPrimitive from "@radix-ui/react-dropdown-menu";
import { Check, ChevronRight, Circle } from "lucide-react";
import type { ComponentProps } from "react";

import { cn } from "./lib/utils";

export const DropdownMenu = DropdownMenuPrimitive.Root;
export const DropdownMenuTrigger = DropdownMenuPrimitive.Trigger;
export const DropdownMenuGroup = DropdownMenuPrimitive.Group;
export const DropdownMenuPortal = DropdownMenuPrimitive.Portal;
export const DropdownMenuSub = DropdownMenuPrimitive.Sub;
export const DropdownMenuRadioGroup = DropdownMenuPrimitive.RadioGroup;

const surface = cn(
  "z-50 min-w-[8rem] overflow-hidden rounded-lg border border-border bg-popover p-1",
  "text-popover-foreground shadow-[var(--glass-shadow)]",
  "data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95",
  "data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95",
  "data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2",
  "data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2",
);

const item = cn(
  "relative flex cursor-default select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none",
  "transition-colors focus:bg-accent focus:text-accent-foreground",
  "data-[disabled]:pointer-events-none data-[disabled]:opacity-50",
  "[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
);

export function DropdownMenuContent({
  className,
  sideOffset = 6,
  ...props
}: ComponentProps<typeof DropdownMenuPrimitive.Content>) {
  return (
    <DropdownMenuPrimitive.Portal>
      <DropdownMenuPrimitive.Content
        className={cn(surface, className)}
        data-slot="dropdown-menu-content"
        sideOffset={sideOffset}
        {...props}
      />
    </DropdownMenuPrimitive.Portal>
  );
}

export function DropdownMenuItem({
  className,
  inset,
  variant = "default",
  ...props
}: ComponentProps<typeof DropdownMenuPrimitive.Item> & {
  inset?: boolean;
  variant?: "danger" | "default";
}) {
  return (
    <DropdownMenuPrimitive.Item
      className={cn(
        item,
        inset && "pl-8",
        variant === "danger" && "text-destructive focus:bg-destructive/10 focus:text-destructive",
        className,
      )}
      data-slot="dropdown-menu-item"
      {...props}
    />
  );
}

export function DropdownMenuCheckboxItem({
  checked,
  children,
  className,
  ...props
}: ComponentProps<typeof DropdownMenuPrimitive.CheckboxItem>) {
  return (
    <DropdownMenuPrimitive.CheckboxItem
      checked={checked}
      className={cn(item, "pl-8", className)}
      data-slot="dropdown-menu-checkbox-item"
      {...props}
    >
      <span className="absolute left-2 flex size-4 items-center justify-center">
        <DropdownMenuPrimitive.ItemIndicator>
          <Check className="size-4" />
        </DropdownMenuPrimitive.ItemIndicator>
      </span>
      {children}
    </DropdownMenuPrimitive.CheckboxItem>
  );
}

export function DropdownMenuRadioItem({
  children,
  className,
  ...props
}: ComponentProps<typeof DropdownMenuPrimitive.RadioItem>) {
  return (
    <DropdownMenuPrimitive.RadioItem
      className={cn(item, "pl-8", className)}
      data-slot="dropdown-menu-radio-item"
      {...props}
    >
      <span className="absolute left-2 flex size-4 items-center justify-center">
        <DropdownMenuPrimitive.ItemIndicator>
          <Circle className="size-2 fill-current" />
        </DropdownMenuPrimitive.ItemIndicator>
      </span>
      {children}
    </DropdownMenuPrimitive.RadioItem>
  );
}

export function DropdownMenuLabel({
  className,
  inset,
  ...props
}: ComponentProps<typeof DropdownMenuPrimitive.Label> & { inset?: boolean }) {
  return (
    <DropdownMenuPrimitive.Label
      className={cn(
        "px-2 py-1.5 font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]",
        inset && "pl-8",
        className,
      )}
      data-slot="dropdown-menu-label"
      {...props}
    />
  );
}

export function DropdownMenuSeparator({
  className,
  ...props
}: ComponentProps<typeof DropdownMenuPrimitive.Separator>) {
  return (
    <DropdownMenuPrimitive.Separator
      className={cn("-mx-1 my-1 h-px bg-border", className)}
      data-slot="dropdown-menu-separator"
      {...props}
    />
  );
}

export function DropdownMenuShortcut({ className, ...props }: ComponentProps<"span">) {
  return (
    <span
      className={cn(
        "ml-auto font-mono text-[10px] text-subtle-foreground tracking-widest",
        className,
      )}
      data-slot="dropdown-menu-shortcut"
      {...props}
    />
  );
}

export function DropdownMenuSubTrigger({
  children,
  className,
  inset,
  ...props
}: ComponentProps<typeof DropdownMenuPrimitive.SubTrigger> & { inset?: boolean }) {
  return (
    <DropdownMenuPrimitive.SubTrigger
      className={cn(item, "data-[state=open]:bg-accent", inset && "pl-8", className)}
      data-slot="dropdown-menu-sub-trigger"
      {...props}
    >
      {children}
      <ChevronRight className="ml-auto size-4" />
    </DropdownMenuPrimitive.SubTrigger>
  );
}

export function DropdownMenuSubContent({
  className,
  ...props
}: ComponentProps<typeof DropdownMenuPrimitive.SubContent>) {
  return (
    <DropdownMenuPrimitive.SubContent
      className={cn(surface, className)}
      data-slot="dropdown-menu-sub-content"
      {...props}
    />
  );
}
