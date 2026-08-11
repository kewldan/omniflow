"use client";

import { Command as CommandPrimitive } from "cmdk";
import { Search } from "lucide-react";
import type { ComponentProps } from "react";

import { Dialog, DialogContent, DialogDescription, DialogTitle } from "./dialog";
import { cn } from "./lib/utils";

export function Command({ className, ...props }: ComponentProps<typeof CommandPrimitive>) {
  return (
    <CommandPrimitive
      className={cn(
        "flex size-full flex-col overflow-hidden rounded-lg bg-popover text-popover-foreground",
        className,
      )}
      data-slot="command"
      {...props}
    />
  );
}

export type CommandDialogProps = ComponentProps<typeof Dialog> & {
  /** Announced to assistive technology; the palette itself has no visible title. */
  description?: string;
  title?: string;
};

export function CommandDialog({
  children,
  description = "Search for a page or an action.",
  title = "Command menu",
  ...props
}: CommandDialogProps) {
  return (
    <Dialog {...props}>
      <DialogContent className="overflow-hidden p-0" hideClose>
        <DialogTitle className="sr-only">{title}</DialogTitle>
        <DialogDescription className="sr-only">{description}</DialogDescription>
        <Command
          className={cn(
            "[&_[cmdk-group-heading]]:px-2 [&_[cmdk-group-heading]]:py-1.5",
            "[&_[cmdk-group-heading]]:font-mono [&_[cmdk-group-heading]]:text-[10px]",
            "[&_[cmdk-group-heading]]:text-subtle-foreground [&_[cmdk-group-heading]]:uppercase",
            "[&_[cmdk-group-heading]]:tracking-[0.14em]",
            "[&_[cmdk-group]]:px-2 [&_[cmdk-input-wrapper]_svg]:size-4",
            "[&_[cmdk-item]]:px-2 [&_[cmdk-item]]:py-2.5 [&_[cmdk-item]_svg]:size-4",
          )}
        >
          {children}
        </Command>
      </DialogContent>
    </Dialog>
  );
}

export function CommandInput({
  className,
  ...props
}: ComponentProps<typeof CommandPrimitive.Input>) {
  return (
    <div className="flex items-center gap-2 border-border border-b px-3" cmdk-input-wrapper="">
      <Search className="size-4 shrink-0 text-subtle-foreground" />
      <CommandPrimitive.Input
        className={cn(
          "flex h-11 w-full bg-transparent py-3 text-sm outline-none",
          "placeholder:text-subtle-foreground disabled:cursor-not-allowed disabled:opacity-50",
          className,
        )}
        data-slot="command-input"
        {...props}
      />
    </div>
  );
}

export function CommandList({ className, ...props }: ComponentProps<typeof CommandPrimitive.List>) {
  return (
    <CommandPrimitive.List
      className={cn("max-h-80 scroll-py-1 overflow-y-auto overflow-x-hidden", className)}
      data-slot="command-list"
      {...props}
    />
  );
}

export function CommandEmpty(props: ComponentProps<typeof CommandPrimitive.Empty>) {
  return (
    <CommandPrimitive.Empty
      className="py-6 text-center text-muted-foreground text-sm"
      data-slot="command-empty"
      {...props}
    />
  );
}

export function CommandGroup({
  className,
  ...props
}: ComponentProps<typeof CommandPrimitive.Group>) {
  return (
    <CommandPrimitive.Group
      className={cn("overflow-hidden p-1 text-foreground", className)}
      data-slot="command-group"
      {...props}
    />
  );
}

export function CommandSeparator({
  className,
  ...props
}: ComponentProps<typeof CommandPrimitive.Separator>) {
  return (
    <CommandPrimitive.Separator
      className={cn("-mx-1 h-px bg-border", className)}
      data-slot="command-separator"
      {...props}
    />
  );
}

export function CommandItem({ className, ...props }: ComponentProps<typeof CommandPrimitive.Item>) {
  return (
    <CommandPrimitive.Item
      className={cn(
        "relative flex cursor-default select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none",
        "data-[selected=true]:bg-accent data-[selected=true]:text-accent-foreground",
        "data-[disabled=true]:pointer-events-none data-[disabled=true]:opacity-50",
        "[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg]:text-muted-foreground",
        className,
      )}
      data-slot="command-item"
      {...props}
    />
  );
}

export function CommandShortcut({ className, ...props }: ComponentProps<"span">) {
  return (
    <span
      className={cn(
        "ml-auto font-mono text-[10px] text-subtle-foreground tracking-widest",
        className,
      )}
      data-slot="command-shortcut"
      {...props}
    />
  );
}
