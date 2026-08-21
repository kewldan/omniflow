"use client";

import { Button } from "@omniflow/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@omniflow/ui/command";
import { Label } from "@omniflow/ui/label";
import { cn } from "@omniflow/ui/lib/utils";
import { Popover, PopoverContent, PopoverTrigger } from "@omniflow/ui/popover";
import { toast } from "@omniflow/ui/toast";
import { Check, ChevronsUpDown } from "lucide-react";
import { useTranslations } from "next-intl";
import { useId, useMemo, useState } from "react";

import { useAccount } from "@/lib/account-session";
import { type ApiError, apiFetch } from "@/lib/api";

/**
 * The timezones a customer can pick from.
 *
 * The browser's own IANA list is used where it exists, so the picker offers
 * exactly what the platform can format in. The short fallback covers a
 * runtime without `Intl.supportedValuesOf`; it is a floor, not the offer, and
 * any stored value outside it is still shown and kept.
 */
function timezoneOptions(current: string): string[] {
  const intl = Intl as unknown as { supportedValuesOf?: (key: string) => string[] };
  let zones: string[] = [];
  try {
    zones = intl.supportedValuesOf ? intl.supportedValuesOf("timeZone") : [];
  } catch {
    zones = [];
  }
  if (zones.length === 0) {
    zones = [
      "UTC",
      "Europe/Moscow",
      "Europe/Kaliningrad",
      "Europe/Samara",
      "Asia/Yekaterinburg",
      "Asia/Omsk",
      "Asia/Novosibirsk",
      "Asia/Krasnoyarsk",
      "Asia/Irkutsk",
      "Asia/Yakutsk",
      "Asia/Vladivostok",
      "Asia/Magadan",
      "Asia/Kamchatka",
      "Europe/Kyiv",
      "Europe/Minsk",
      "Asia/Almaty",
      "Asia/Tashkent",
      "Asia/Tbilisi",
      "Asia/Yerevan",
      "Asia/Baku",
      "Europe/Istanbul",
      "Europe/Berlin",
      "Europe/London",
      "America/New_York",
      "America/Los_Angeles",
      "Asia/Dubai",
      "Asia/Bangkok",
      "Asia/Shanghai",
      "Asia/Tokyo",
    ];
  }
  if (!zones.includes("UTC")) {
    zones = ["UTC", ...zones];
  }
  if (current && !zones.includes(current)) {
    zones = [current, ...zones];
  }
  return zones;
}

/** The current UTC offset of a zone, as "+03:00", for the reader who thinks in offsets. */
function offsetOf(zone: string): string {
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: zone,
      timeZoneName: "longOffset",
    }).formatToParts(new Date());
    const name = parts.find((part) => part.type === "timeZoneName")?.value ?? "";
    return name === "GMT" ? "+00:00" : name.replace("GMT", "");
  } catch {
    return "";
  }
}

/**
 * The account's timezone, as a searchable Combobox.
 *
 * Quiet hours are computed in `users.timezone`, and every screen that mentions
 * them says "in your own timezone" — which was untrue while nothing let the
 * customer set it and every account sat on UTC. A closed `Select` of four
 * hundred zones is not a control anybody can use; a Combobox built from
 * `Command` and `Popover` is searchable by name and offset, and it is the
 * design system's control rather than the browser's.
 *
 * The value is written through `PATCH /v1/account/me` together with the
 * account's current language, because that route takes both and the language
 * must not be reset by a timezone change.
 */
export function TimezoneField() {
  const translate = useTranslations("account.timezone");
  const { refresh, session } = useAccount();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const id = useId();

  const current = session?.customer.timezone ?? "UTC";
  const locale = session?.customer.locale ?? "en";
  const zones = useMemo(() => timezoneOptions(current), [current]);
  const detected = useMemo(() => {
    try {
      return Intl.DateTimeFormat().resolvedOptions().timeZone;
    } catch {
      return "";
    }
  }, []);

  async function save(next: string) {
    setOpen(false);
    if (next === current) {
      return;
    }
    setBusy(true);
    try {
      await apiFetch("/v1/account/me", {
        body: JSON.stringify({ locale, timezone: next }),
        method: "PATCH",
      });
      await refresh();
      toast.success(translate("saved", { zone: next }));
    } catch (saveError) {
      toast.error((saveError as ApiError).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-2 rounded-xl border border-border bg-card p-4">
      <Label htmlFor={id}>{translate("label")}</Label>
      <p className="text-[12px] text-muted-foreground leading-relaxed">{translate("hint")}</p>
      <Popover onOpenChange={setOpen} open={open}>
        <PopoverTrigger asChild>
          <Button
            aria-expanded={open}
            className="w-full justify-between font-normal"
            disabled={busy}
            id={id}
            role="combobox"
            variant="outline"
          >
            <span className="truncate">
              {current}
              <span className="ml-2 font-mono text-[11px] text-subtle-foreground">
                {offsetOf(current)}
              </span>
            </span>
            <ChevronsUpDown aria-hidden className="size-4 shrink-0 opacity-60" />
          </Button>
        </PopoverTrigger>
        <PopoverContent align="start" className="w-[--radix-popover-trigger-width] p-0">
          <Command>
            <CommandInput placeholder={translate("search")} />
            <CommandList>
              <CommandEmpty>{translate("empty")}</CommandEmpty>
              {detected && detected !== current && zones.includes(detected) && (
                <CommandGroup heading={translate("detected")}>
                  <CommandItem onSelect={() => save(detected)} value={`detected ${detected}`}>
                    <span className="flex-1 truncate">{detected}</span>
                    <span className="font-mono text-[11px] text-subtle-foreground">
                      {offsetOf(detected)}
                    </span>
                  </CommandItem>
                </CommandGroup>
              )}
              <CommandGroup heading={translate("all")}>
                {zones.map((zone) => (
                  <CommandItem key={zone} onSelect={() => save(zone)} value={zone}>
                    <Check
                      aria-hidden
                      className={cn("size-4", zone === current ? "opacity-100" : "opacity-0")}
                    />
                    <span className="flex-1 truncate">{zone}</span>
                    <span className="font-mono text-[11px] text-subtle-foreground">
                      {offsetOf(zone)}
                    </span>
                  </CommandItem>
                ))}
              </CommandGroup>
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
    </div>
  );
}
