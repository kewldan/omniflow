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
import { Search, ShieldCheck, UserPlus } from "lucide-react";
import dynamic from "next/dynamic";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { useTheme } from "next-themes";
import { useEffect, useState } from "react";

import { NAVIGATION } from "@/lib/navigation";
import { useSession } from "@/lib/session";

// Record search is fetched the first time the palette opens, not with the
// shell that every panel route ships.
const RecordResults = dynamic(
  () => import("./record-results").then((module) => module.RecordResults),
  {
    ssr: false,
  },
);

/**
 * Command search.
 *
 * Opened with Ctrl/Cmd+K from anywhere, and by a visible button — a full field
 * on wide screens, an icon on a phone — so the feature is neither keyboard-only
 * knowledge nor missing on mobile. It does three things: jumps to a screen,
 * finds a record, and starts a common action. Screens and actions the operator
 * lacks permission for are filtered out, matching the sidebar; record groups
 * are filtered by the API, which never returns a group the operator could not
 * open.
 */
export function CommandMenu() {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [searching, setSearching] = useState(false);
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

  function changeOpen(next: boolean) {
    setOpen(next);
    if (!next) {
      setSearch("");
      setSearching(false);
    }
  }

  function run(action: () => void) {
    changeOpen(false);
    action();
  }

  const actions = [
    can("admins.write") && {
      href: "/admin/operators?invite=1",
      icon: UserPlus,
      key: "inviteOperator",
    },
    { href: "/admin/security", icon: ShieldCheck, key: "security" },
  ].filter(Boolean) as { href: string; icon: typeof UserPlus; key: string }[];

  return (
    <>
      <Button
        aria-keyshortcuts="Control+K"
        className="hidden w-full max-w-64 justify-start gap-2 text-muted-foreground md:inline-flex"
        onClick={() => setOpen(true)}
        size="sm"
        variant="outline"
      >
        <Search className="size-4" />
        <span className="truncate">{translate("commandMenu.trigger")}</span>
        <CommandShortcut className="ml-auto">⌘K</CommandShortcut>
      </Button>
      <Button
        aria-label={translate("commandMenu.trigger")}
        className="md:hidden"
        onClick={() => setOpen(true)}
        size="icon-sm"
        variant="ghost"
      >
        <Search />
      </Button>

      <CommandDialog
        description={translate("commandMenu.description")}
        onOpenChange={changeOpen}
        open={open}
        title={translate("commandMenu.title")}
      >
        <CommandInput
          onValueChange={setSearch}
          placeholder={translate("commandMenu.placeholder")}
          value={search}
        />
        <CommandList>
          {searching ? null : <CommandEmpty>{translate("commandMenu.empty")}</CommandEmpty>}
          {open ? (
            <RecordResults
              onPendingChange={setSearching}
              onSelect={(href) => run(() => router.push(href))}
              search={search}
            />
          ) : null}

          {actions.length > 0 ? (
            <CommandGroup heading={translate("commandMenu.actions")}>
              {actions.map((action) => {
                const Icon = action.icon;
                return (
                  <CommandItem
                    key={action.key}
                    onSelect={() => run(() => router.push(action.href))}
                    value={translate(`commandMenu.actionItems.${action.key}`)}
                  >
                    <Icon aria-hidden />
                    {translate(`commandMenu.actionItems.${action.key}`)}
                  </CommandItem>
                );
              })}
            </CommandGroup>
          ) : null}

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
