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
import { cn } from "@omniflow/ui/lib/utils";
import { Popover, PopoverContent, PopoverTrigger } from "@omniflow/ui/popover";
import { Check, ChevronsUpDown } from "lucide-react";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import type { FieldControlProps } from "@/components/forms/field";
import { type ApiError, fetcher } from "@/lib/api";

/** One squad as the Remnawave panel reports it. */
export type RemnawaveSquad = { uuid: string; name: string; members: number };

/** The panel's squads, read live; `reported` is false when it could not be read. */
export type SquadListing = {
  reported: boolean;
  detail?: string;
  internal: RemnawaveSquad[];
  external: RemnawaveSquad[];
};

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * The Remnawave squads, read once per editor.
 *
 * The list is never stored: Remnawave owns squads, and a copy kept here would
 * be the thing that drifts.
 */
export function useSquads() {
  return useSWR<SquadListing, ApiError>("/v1/panel/remnawave/squads", fetcher, {
    revalidateOnFocus: false,
  });
}

/**
 * A searchable picker over the panel's squads.
 *
 * It picks by name rather than by pasted identifier, which is where a wrong
 * squad used to come from. An identifier typed into the search is still
 * accepted when it is a well-formed UUID, so an operator whose panel did not
 * answer — or who knows a squad the list does not show yet — is not stuck.
 */
export function SquadPicker({
  control,
  kind = "internal",
  multiple,
  onChange,
  value,
}: {
  control: FieldControlProps;
  kind?: "internal" | "external";
  multiple: boolean;
  onChange: (next: string[]) => void;
  value: string[];
}) {
  const translate = useTranslations("admin.catalog.squadPicker");
  const { data } = useSquads();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const squads = (kind === "external" ? data?.external : data?.internal) ?? [];
  const names = new Map(squads.map((squad) => [squad.uuid, squad.name]));

  function toggle(uuid: string) {
    const normalized = uuid.toLowerCase();
    if (!multiple) {
      onChange(value[0] === normalized ? [] : [normalized]);
      setOpen(false);
      return;
    }
    onChange(
      value.includes(normalized)
        ? value.filter((entry) => entry !== normalized)
        : [...value, normalized],
    );
  }

  const label =
    value.length === 0
      ? translate(multiple ? "none" : "choose")
      : value.map((uuid) => names.get(uuid) ?? `${uuid.slice(0, 8)}…`).join(", ");
  const typed = search.trim();

  return (
    <div className="flex flex-col gap-1">
      <Popover onOpenChange={setOpen} open={open}>
        <PopoverTrigger asChild>
          <Button
            {...control}
            aria-expanded={open}
            className="w-full justify-between font-normal"
            role="combobox"
            type="button"
            variant="outline"
          >
            <span className="truncate">{label}</span>
            <ChevronsUpDown aria-hidden className="size-4 shrink-0 opacity-60" />
          </Button>
        </PopoverTrigger>
        <PopoverContent align="start" className="w-[--radix-popover-trigger-width] p-0">
          <Command>
            <CommandInput
              onValueChange={setSearch}
              placeholder={translate("search")}
              value={search}
            />
            <CommandList>
              <CommandEmpty>{translate("empty")}</CommandEmpty>
              {UUID.test(typed) && !names.has(typed.toLowerCase()) && (
                <CommandGroup heading={translate("typed")}>
                  <CommandItem onSelect={() => toggle(typed)} value={typed}>
                    <span className="font-mono text-xs">{typed}</span>
                  </CommandItem>
                </CommandGroup>
              )}
              <CommandGroup>
                {squads.map((squad) => (
                  <CommandItem
                    key={squad.uuid}
                    onSelect={() => toggle(squad.uuid)}
                    value={`${squad.name} ${squad.uuid}`}
                  >
                    <Check
                      aria-hidden
                      className={cn(
                        "size-4",
                        value.includes(squad.uuid) ? "opacity-100" : "opacity-0",
                      )}
                    />
                    <span className="flex-1 truncate">{squad.name}</span>
                    <span className="text-[11px] text-subtle-foreground">
                      {translate("members", { count: squad.members })}
                    </span>
                  </CommandItem>
                ))}
              </CommandGroup>
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
      {data && !data.reported && (
        <span className="text-muted-foreground text-xs">{translate("unavailable")}</span>
      )}
    </div>
  );
}

/** The name the panel gives a squad, for pre-filling a customer-facing label. */
export function squadName(listing: SquadListing | undefined, uuid: string): string | undefined {
  return listing?.internal.find((squad) => squad.uuid === uuid)?.name;
}
