"use client";

import { CalendarDays } from "lucide-react";
import { useId, useState } from "react";

import { Button } from "./button";
import { Calendar } from "./calendar";
import { cn } from "./lib/utils";
import { Popover, PopoverContent, PopoverTrigger } from "./popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./select";

/**
 * Picks a moment: a day from the calendar, an hour and a minute from two lists.
 *
 * Every place this replaces used `<input type="datetime-local">`, which renders
 * in the browser's own chrome and therefore looks like a different product on
 * each platform. The time half is two selects rather than a text field because
 * a free-text time is a parsing problem the operator has to solve — "9", "9pm",
 * "21:00" — and the answer to every one of them is a list.
 *
 * The value is a local wall-clock string, `YYYY-MM-DDTHH:mm`, which is exactly
 * what the input it replaces produced. Callers already turn that into an
 * instant with `new Date(value).toISOString()`, so nothing downstream changes.
 */
export type DateTimeFieldProps = {
  /** `YYYY-MM-DDTHH:mm`, or empty for no value. */
  value: string;
  onChange: (value: string) => void;
  /** Shown on the trigger when there is no value. */
  placeholder: string;
  /** Labels the two time lists for a screen reader. */
  hourLabel: string;
  minuteLabel: string;
  id?: string;
  disabled?: boolean;
  className?: string;
  /** Days before this are not selectable. */
  fromDate?: Date;
};

/** Minutes are offered every five, which is the granularity anybody schedules at. */
const MINUTE_STEP = 5;

const HOURS = Array.from({ length: 24 }, (_, hour) => String(hour).padStart(2, "0"));
const MINUTES = Array.from({ length: 60 / MINUTE_STEP }, (_, index) =>
  String(index * MINUTE_STEP).padStart(2, "0"),
);

/** Splits the stored value without going through Date, which would apply a zone. */
function parse(value: string): { date?: Date; hour: string; minute: string } {
  const match = /^(\d{4})-(\d{2})-(\d{2})(?:T(\d{2}):(\d{2}))?$/.exec(value);
  if (!match) {
    return { hour: "09", minute: "00" };
  }
  const [, year, month, day, hour, minute] = match;
  return {
    date: new Date(Number(year), Number(month) - 1, Number(day)),
    hour: hour ?? "09",
    // Snap to the offered granularity so a value set elsewhere still selects.
    minute: minute
      ? String(Math.round(Number(minute) / MINUTE_STEP) * MINUTE_STEP).padStart(2, "0")
      : "00",
  };
}

function format(date: Date, hour: string, minute: string): string {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}T${hour}:${minute}`;
}

export function DateTimeField({
  className,
  disabled,
  fromDate,
  hourLabel,
  id,
  minuteLabel,
  onChange,
  placeholder,
  value,
}: DateTimeFieldProps) {
  const [open, setOpen] = useState(false);
  const generatedId = useId();
  const fieldId = id ?? generatedId;
  const { date, hour, minute } = parse(value);

  function commit(nextDate: Date | undefined, nextHour: string, nextMinute: string) {
    if (!nextDate) {
      onChange("");
      return;
    }
    onChange(format(nextDate, nextHour, nextMinute));
  }

  return (
    <Popover onOpenChange={setOpen} open={open}>
      <PopoverTrigger asChild>
        <Button
          className={cn(
            "w-full justify-start font-normal",
            !date && "text-subtle-foreground",
            className,
          )}
          disabled={disabled}
          id={fieldId}
          type="button"
          variant="outline"
        >
          <CalendarDays />
          {date ? `${date.toLocaleDateString()} ${hour}:${minute}` : placeholder}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-auto p-3">
        <div className="flex flex-col gap-3">
          <Calendar
            autoFocus
            disabled={fromDate ? { before: fromDate } : undefined}
            mode="single"
            onSelect={(selected) => {
              commit(selected, hour, minute);
              // Choosing a day is the common case and usually the last one, so
              // the popover closes on it. The time lists stay reachable by
              // reopening, which is the rarer edit.
              if (selected) {
                setOpen(false);
              }
            }}
            selected={date}
          />
          <div className="flex items-center gap-2">
            <Select
              disabled={!date}
              onValueChange={(nextHour) => commit(date, nextHour, minute)}
              value={hour}
            >
              <SelectTrigger aria-label={hourLabel} className="w-20">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {HOURS.map((option) => (
                  <SelectItem key={option} value={option}>
                    {option}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <span aria-hidden className="text-subtle-foreground">
              :
            </span>
            <Select
              disabled={!date}
              onValueChange={(nextMinute) => commit(date, hour, nextMinute)}
              value={minute}
            >
              <SelectTrigger aria-label={minuteLabel} className="w-20">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {MINUTES.map((option) => (
                  <SelectItem key={option} value={option}>
                    {option}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}
