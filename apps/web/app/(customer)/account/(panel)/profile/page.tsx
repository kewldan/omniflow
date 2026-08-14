"use client";

import { Button } from "@omniflow/ui/button";
import { Label } from "@omniflow/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@omniflow/ui/select";
import { toast } from "@omniflow/ui/toast";
import { BellRing, Gift, MonitorSmartphone, Moon, Shield, ShieldCheck, Sun } from "lucide-react";
import { useRouter } from "next/navigation";
import { useLocale, useTranslations } from "next-intl";
import { useTheme } from "next-themes";
import { useState } from "react";

import { HubNav } from "@/components/account/account/hub-nav";
import { SectionLabel } from "@/components/account/state";
import { useAllowedThemes } from "@/components/theme-provider";
import { useAccount } from "@/lib/account-session";
import { type ApiError, apiFetch } from "@/lib/api";

/**
 * The account screen: who you are, the two preferences that follow you between
 * devices, and the way into everything that is not a tab.
 *
 * Theme and language live here rather than in a header control, exactly as the
 * source design places them — they are set once, and a persistent toggle in the
 * chrome would spend space on something nobody changes twice.
 *
 * The link groups below them are the hub. Five tabs cannot carry devices,
 * sessions, invites, notification preferences, and personal data as well, and
 * each of those is opened rarely enough that a menu entry is the right cost. The
 * groups are ordered by how often they are actually needed rather than by which
 * subsystem owns them.
 */
export default function ProfilePage() {
  const translate = useTranslations("account");
  // A second namespace, not a duplicate: v0.10's copy lives in its own fragment
  // so the screens shipping it can be merged independently of the v0.9 catalogue
  // this page was written against.
  const hub = useTranslations("account.account");
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

      <ThemeChoice />

      <SectionLabel>{translate("profile.language")}</SectionLabel>
      <LanguageChoice
        current={customer?.locale ?? "en"}
        onSaved={refresh}
        timezone={customer?.timezone ?? "UTC"}
      />

      <SectionLabel>{translate("profile.security")}</SectionLabel>
      {/* Devices reached the tab bar in v0.9 and left it in v0.10, because
          disconnecting a replaced phone is something a person does once rather
          than something they come back to. It is reachable from here and from
          the subscription it belongs to, so nothing lost a way in. */}
      <HubNav
        items={[
          {
            description: hub("hub.devicesHint"),
            href: "/account/devices",
            icon: MonitorSmartphone,
            label: translate("nav.devices"),
          },
          {
            description: hub("hub.sessionsHint"),
            href: "/account/security",
            icon: Shield,
            label: translate("profile.sessions"),
          },
        ]}
        label={translate("profile.security")}
      />

      <SectionLabel>{hub("hub.rewards")}</SectionLabel>
      <HubNav
        items={[
          {
            description: hub("hub.referralsHint"),
            href: "/account/referrals",
            icon: Gift,
            label: hub("hub.referrals"),
          },
        ]}
        label={hub("hub.rewards")}
      />

      <SectionLabel>{hub("hub.data")}</SectionLabel>
      {/* Notification preferences and personal data are one group because they
          are the same decision seen from two sides: what may be sent to you, and
          what is held about you. */}
      <HubNav
        items={[
          {
            description: hub("hub.preferencesHint"),
            href: "/account/preferences",
            icon: BellRing,
            label: hub("hub.preferences"),
          },
          {
            description: hub("hub.privacyHint"),
            href: "/account/privacy",
            icon: ShieldCheck,
            label: hub("hub.privacy"),
          },
        ]}
        label={hub("hub.data")}
      />
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
  const allowedThemes = useAllowedThemes();

  // An installation offering one mode has nothing to choose here. A pair of
  // buttons where one of them cannot take effect is worse than no buttons.
  const options = (["dark", "light"] as const).filter((option) => allowedThemes.includes(option));
  if (options.length < 2) {
    return null;
  }

  // The heading belongs to the control rather than to the page, so an
  // installation with one mode loses both together instead of leaving a
  // section label above nothing.
  return (
    <>
      <SectionLabel>{translate("profile.appearance")}</SectionLabel>
      <fieldset className="flex rounded-full border border-border bg-card p-1">
        <legend className="sr-only">{translate("profile.appearance")}</legend>
        {options.map((option) => (
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
    </>
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
