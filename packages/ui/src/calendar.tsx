"use client";

import { ChevronLeft, ChevronRight } from "lucide-react";
import type { ComponentProps } from "react";
import { DayPicker } from "react-day-picker";

import { buttonVariants } from "./button";
import { cn } from "./lib/utils";

/**
 * The date picker.
 *
 * It exists because a native `<input type="date">` renders in the browser's own
 * chrome: it ignores every token in this package, and it looks and behaves
 * differently on each platform, which is the thing a shared component system is
 * for. The same applies to `type="time"` and `type="datetime-local"`.
 *
 * The day cells reuse `buttonVariants` rather than restating their own hover,
 * focus, and pressed treatments, so a change to how a control feels reaches the
 * calendar without anybody remembering to come here.
 */
export function Calendar({
  className,
  classNames,
  showOutsideDays = true,
  ...props
}: ComponentProps<typeof DayPicker>) {
  return (
    <DayPicker
      className={cn("p-1", className)}
      classNames={{
        button_next: cn(
          buttonVariants({ size: "icon-sm", variant: "ghost" }),
          "absolute right-1 top-0",
        ),
        button_previous: cn(
          buttonVariants({ size: "icon-sm", variant: "ghost" }),
          "absolute left-1 top-0",
        ),
        caption_label: "font-medium text-sm",
        day: "relative p-0 text-center",
        day_button: cn(
          buttonVariants({ size: "icon-sm", variant: "ghost" }),
          "size-8 font-normal aria-selected:opacity-100",
        ),
        disabled: "text-subtle-foreground opacity-50",
        hidden: "invisible",
        month: "flex flex-col gap-3",
        month_caption: "relative flex h-8 items-center justify-center",
        month_grid: "w-full border-collapse",
        months: "relative flex flex-col gap-3",
        // Outside days are shown but recede, so the grid keeps its shape
        // without inviting a click on a month the operator is not looking at.
        outside: "text-subtle-foreground opacity-50",
        selected: cn(
          "[&>button]:bg-primary [&>button]:text-primary-foreground",
          "[&>button:hover]:bg-primary [&>button:hover]:text-primary-foreground",
        ),
        // Today is marked by weight rather than by a colour, because the
        // selected day already owns the accent and two accents compete.
        today: "[&>button]:font-semibold [&>button]:underline [&>button]:underline-offset-4",
        week: "flex w-full",
        weekday: "w-8 font-normal text-[11px] text-subtle-foreground",
        weekdays: "flex",
        ...classNames,
      }}
      components={{
        Chevron: ({ orientation, ...chevron }) =>
          orientation === "left" ? (
            <ChevronLeft className="size-4" {...chevron} />
          ) : (
            <ChevronRight className="size-4" {...chevron} />
          ),
      }}
      showOutsideDays={showOutsideDays}
      {...props}
    />
  );
}
