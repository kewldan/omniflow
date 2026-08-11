"use client";

import { Button } from "@omniflow/ui/button";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { toast } from "@omniflow/ui/toast";
import { ChevronRight, Moon, Shield, Sun } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useLocale, useTranslations } from "next-intl";
import { useTheme } from "next-themes";
import { useState } from "react";

import { SectionLabel } from "@/components/account/state";
import { useAccount } from "@/lib/account-session";
import { type ApiError, apiFetch } from "@/lib/api";

/**
 * The account screen: who you are, and the two preferences that follow you
 * between devices.
 *
 * Theme and language live here rather than in a header control, exactly as the
 * source design places them — they are set once, and a persistent toggle in the
 * chrome would spend space on something nobody changes twice.
 */
export default function ProfilePage() {
  const translate = useTranslations("account");
  const { refresh, session } = useAccount();
  const customer = session?.customer;

  return (
    <div className="animate-step-in space-y-5">
      <section className="rounded-xl border border-border bg-card p-4">
        <p className="font-mono text-[11px] text-subtle-foreground">
          {translate("profile.signedIn")}
        </p>
        <p className="mt-1.5 font-semibold text-[15px]">
          {translate(`profile.method.${session?.session.authMethod ?? "telegram"}`, {
            provider: session?.session.authProvider ?? "",
          })}
        </p>
      </section>

      <SectionLabel>{translate("profile.appearance")}</SectionLabel>
      <ThemeChoice />

      <SectionLabel>{translate("profile.language")}</SectionLabel>
      <LanguageChoice
        current={customer?.locale ?? "en"}
        onSaved={refresh}
        timezone={customer?.timezone ?? "UTC"}
      />

      <SectionLabel>{translate("profile.security")}</SectionLabel>
      <nav className="overflow-hidden rounded-xl border border-border bg-card">
        <Link
          className="flex items-center gap-3 px-4 py-3.5 transition-colors hover:bg-accent"
          href="/account/security"
        >
          <Shield aria-hidden className="size-4 text-muted-foreground" />
          <span className="flex-1 font-semibold text-[15px]">{translate("profile.sessions")}</span>
          <ChevronRight aria-hidden className="size-4 text-subtle-foreground" />
        </Link>
      </nav>
    </div>
  );
}

/**
 * The theme toggle.
 *
 * It stays a device preference rather than an account one: the same person
 * reasonably wants dark on a phone at night and light on a laptop, and syncing
 * it across devices would make that impossible.
 */
function ThemeChoice() {
  const translate = useTranslations("account");
  const { setTheme, theme } = useTheme();

  return (
    <fieldset className="flex rounded-full border border-border bg-card p-1">
      <legend className="sr-only">{translate("profile.appearance")}</legend>
      {(["dark", "light"] as const).map((option) => (
        <Button
          aria-pressed={theme === option}
          className="flex-1 rounded-full"
          key={option}
          onClick={() => setTheme(option)}
          size="sm"
          variant={theme === option ? "secondary" : "ghost"}
        >
          {option === "dark" ? <Moon aria-hidden /> : <Sun aria-hidden />}
          {translate(`profile.theme.${option}`)}
        </Button>
      ))}
    </fieldset>
  );
}

/**
 * The language choice.
 *
 * Unlike the theme this is stored on the account, because it also decides the
 * language of the messages the bot sends — a preference that has to survive the
 * browser it was set in.
 */
function LanguageChoice({
  current,
  onSaved,
  timezone,
}: {
  current: "en" | "ru";
  onSaved: () => void;
  timezone: string;
}) {
  const translate = useTranslations("account");
  const locale = useLocale();
  const router = useRouter();
  const [busy, setBusy] = useState(false);

  async function save(next: string) {
    if (next === current) {
      return;
    }
    setBusy(true);
    try {
      await apiFetch("/v1/account/me", {
        body: JSON.stringify({ locale: next, timezone }),
        method: "PATCH",
      });
      onSaved();
      // The rendered locale comes from the request, so the page is refreshed to
      // pick up catalogues for the language just chosen.
      router.refresh();
      toast.success(translate("profile.languageSaved"));
    } catch (saveError) {
      toast.error((saveError as ApiError).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-2 rounded-xl border border-border bg-card p-4">
      <Label htmlFor="account-locale">{translate("profile.language")}</Label>
      <Select defaultValue={current} disabled={busy} onValueChange={save}>
        <SelectTrigger id="account-locale">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="ru">Русский</SelectItem>
          <SelectItem value="en">English</SelectItem>
        </SelectContent>
      </Select>
      <p className="font-mono text-[11px] text-subtle-foreground">
        {translate("profile.languageHint", { current: locale })}
      </p>
    </div>
  );
}
