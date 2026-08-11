"use client";

import { Button } from "@omniflow/ui/button";
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandShortcut,
} from "@omniflow/ui/command";
import { Search } from "lucide-react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { useTheme } from "next-themes";
import { useEffect, useState } from "react";

import { NAVIGATION } from "@/lib/navigation";
import { useSession } from "@/lib/session";

/**
 * Command search.
 *
 * Opened with Ctrl/Cmd+K from anywhere, and also reachable by a visible button
 * so the feature is not keyboard-only knowledge. Entries the operator lacks
 * permission for are filtered out, matching the sidebar.
 */
export function CommandMenu() {
  const [open, setOpen] = useState(false);
  const router = useRouter();
  const translate = useTranslations("admin");
  const { can } = useSession();
  const { setTheme } = useTheme();

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "k" && (event.metaKey || event.ctrlKey)) {
        event.preventDefault();
        setOpen((previous) => !previous);
      }
    }
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);

  function run(action: () => void) {
    setOpen(false);
    action();
  }

  return (
    <>
      <Button
        aria-keyshortcuts="Control+K"
        className="w-full max-w-64 justify-start gap-2 text-muted-foreground"
        onClick={() => setOpen(true)}
        size="sm"
        variant="outline"
      >
        <Search className="size-4" />
        <span className="truncate">{translate("commandMenu.trigger")}</span>
        <CommandShortcut className="ml-auto">⌘K</CommandShortcut>
      </Button>

      <CommandDialog
        description={translate("commandMenu.description")}
        onOpenChange={setOpen}
        open={open}
        title={translate("commandMenu.title")}
      >
        <CommandInput placeholder={translate("commandMenu.placeholder")} />
        <CommandList>
          <CommandEmpty>{translate("commandMenu.empty")}</CommandEmpty>

          {NAVIGATION.map((section) => {
            const visible = section.items.filter(
              (item) => !item.planned && (!item.permission || can(item.permission)),
            );
            if (visible.length === 0) {
              return null;
            }
            return (
              <CommandGroup
                heading={translate(`navigation.sections.${section.key}`)}
                key={section.key}
              >
                {visible.map((item) => {
                  const Icon = item.icon;
                  return (
                    <CommandItem
                      key={item.key}
                      onSelect={() => run(() => router.push(item.href))}
                      value={translate(`navigation.items.${item.key}`)}
                    >
                      <Icon aria-hidden="true" />
                      {translate(`navigation.items.${item.key}`)}
                    </CommandItem>
                  );
                })}
              </CommandGroup>
            );
          })}

          <CommandGroup heading={translate("commandMenu.appearance")}>
            <CommandItem
              onSelect={() => run(() => setTheme("light"))}
              value={translate("theme.light")}
            >
              {translate("theme.light")}
            </CommandItem>
            <CommandItem
              onSelect={() => run(() => setTheme("dark"))}
              value={translate("theme.dark")}
            >
              {translate("theme.dark")}
            </CommandItem>
            <CommandItem
              onSelect={() => run(() => setTheme("system"))}
              value={translate("theme.system")}
            >
              {translate("theme.system")}
            </CommandItem>
          </CommandGroup>
        </CommandList>
      </CommandDialog>
    </>
  );
}
