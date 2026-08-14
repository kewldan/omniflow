"use client";

import { Avatar, AvatarFallback } from "@omniflow/ui/avatar";
import { Button } from "@omniflow/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@omniflow/ui/dropdown-menu";
import { toast } from "@omniflow/ui/toast";
import { LogOut, Monitor, Moon, ShieldCheck, Sun } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { useTheme } from "next-themes";
import { useState } from "react";

import { useAllowedThemes } from "@/components/theme-provider";
import { apiFetch } from "@/lib/api";
import { useSession } from "@/lib/session";

/** Two initials from a display name, for the avatar fallback. */
function initials(displayName: string): string {
  const parts = displayName.trim().split(/\s+/).slice(0, 2);
  return parts.map((part) => part.charAt(0).toUpperCase()).join("") || "?";
}

export function UserMenu() {
  const { session, refresh } = useSession();
  const translate = useTranslations("admin");
  const { setTheme } = useTheme();
  const allowedThemes = useAllowedThemes();
  const router = useRouter();
  const [signingOut, setSigningOut] = useState(false);

  if (!session) {
    return null;
  }

  async function signOut() {
    setSigningOut(true);
    try {
      await apiFetch("/v1/panel/auth/logout", { method: "POST" });
      await refresh();
      router.push("/admin/login");
    } catch {
      toast.error(translate("userMenu.signOutFailed"));
      setSigningOut(false);
    }
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          aria-label={translate("userMenu.label")}
          className="gap-2 px-1.5"
          size="sm"
          variant="ghost"
        >
          <Avatar className="size-6">
            <AvatarFallback>{initials(session.account.displayName)}</AvatarFallback>
          </Avatar>
          <span className="hidden max-w-32 truncate sm:inline">{session.account.displayName}</span>
        </Button>
      </DropdownMenuTrigger>

      <DropdownMenuContent align="end" className="w-60">
        <div className="px-2 py-1.5">
          <p className="truncate font-medium text-sm">{session.account.displayName}</p>
          <p className="truncate font-mono text-[11px] text-subtle-foreground">
            {session.account.email}
          </p>
          <p className="mt-1 text-muted-foreground text-xs">
            {session.account.roles.map((role) => translate(`roles.${role}`)).join(", ")}
          </p>
        </div>

        <DropdownMenuSeparator />
        <DropdownMenuItem asChild>
          <Link href="/admin/security">
            <ShieldCheck />
            {translate("userMenu.security")}
          </Link>
        </DropdownMenuItem>

        {/*
          An installation that offers one mode has nothing to choose, so the
          whole section goes rather than leaving three items that cannot change
          anything.
        */}
        {allowedThemes.length > 1 ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel>{translate("theme.label")}</DropdownMenuLabel>
            {allowedThemes.includes("light") ? (
              <DropdownMenuItem onSelect={() => setTheme("light")}>
                <Sun />
                {translate("theme.light")}
              </DropdownMenuItem>
            ) : null}
            {allowedThemes.includes("dark") ? (
              <DropdownMenuItem onSelect={() => setTheme("dark")}>
                <Moon />
                {translate("theme.dark")}
              </DropdownMenuItem>
            ) : null}
            <DropdownMenuItem onSelect={() => setTheme("system")}>
              <Monitor />
              {translate("theme.system")}
            </DropdownMenuItem>
          </>
        ) : null}

        <DropdownMenuSeparator />
        <DropdownMenuItem disabled={signingOut} onSelect={signOut} variant="danger">
          <LogOut />
          {translate("userMenu.signOut")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
