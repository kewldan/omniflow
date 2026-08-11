"use client";

import { Button } from "@omniflow/ui/button";
import { Skeleton } from "@omniflow/ui/skeleton";
import { toast } from "@omniflow/ui/toast";
import { MessageCircle } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { useEffect, useState } from "react";
import useSWR from "swr";

import { type ApiError, apiFetch, fetcher } from "@/lib/api";

type SignInMethods = {
  telegram: boolean;
  /** The bot's own @name, which is what the login widget is loaded by. */
  telegramBot?: string;
  magicLink: boolean;
  oidc: { slug: string; displayName: string; icon?: string }[];
};

/**
 * The sign-in screen.
 *
 * It renders from what the installation actually offers rather than from what
 * the code can do, so an installation with no bot token never shows a Telegram
 * button that cannot work and one with three OIDC providers shows three.
 */
export default function SignInPage() {
  const translate = useTranslations("account");
  const search = useSearchParams();
  const router = useRouter();
  const { data, isLoading } = useSWR<SignInMethods, ApiError>("/v1/account/auth/methods", fetcher);
  const [busy, setBusy] = useState(false);

  const reason = search.get("error");

  // Opened inside Telegram, the surrounding client has already signed the
  // customer's identity, so the widget is unnecessary and this signs in
  // immediately. Outside Telegram the global is absent and nothing happens.
  useEffect(() => {
    const initData = readMiniAppInitData();
    if (!initData || busy) {
      return;
    }
    setBusy(true);
    apiFetch("/v1/account/auth/telegram/miniapp", {
      body: JSON.stringify({ initData }),
      method: "POST",
    })
      .then(() => router.replace("/account"))
      .catch(() => setBusy(false));
  }, [busy, router]);

  return (
    <main className="mx-auto flex min-h-dvh max-w-sm flex-col justify-center gap-6 px-6 py-16">
      <header className="space-y-2">
        <p className="font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.16em]">
          {translate("nav.title")}
        </p>
        <h1 className="font-bold text-[26px] tracking-[-0.035em]">{translate("signIn.title")}</h1>
        <p className="text-[14px] text-muted-foreground leading-relaxed">
          {translate("signIn.description")}
        </p>
      </header>

      {/* A failed round trip comes back as a reason code in the URL. Each maps to
          a sentence that says what to do next, rather than to a bare "failed". */}
      {reason && (
        <p
          className="rounded-lg border border-destructive/40 bg-destructive/10 px-4 py-3 text-[13px]"
          role="alert"
        >
          {translate(`signIn.errors.${reason}`, { fallback: translate("signIn.errors.generic") })}
        </p>
      )}

      {isLoading ? (
        <div className="space-y-3">
          <Skeleton className="h-11 w-full rounded-md" />
          <Skeleton className="h-11 w-full rounded-md" />
        </div>
      ) : (
        <div className="space-y-3">
          {data?.telegram && data.telegramBot && (
            <TelegramButton botUsername={data.telegramBot} disabled={busy} />
          )}
          {(data?.oidc ?? []).map((provider) => (
            <Button asChild className="w-full" key={provider.slug} size="lg" variant="outline">
              <a href={`/v1/account/auth/oidc/${provider.slug}/start`}>
                {translate("signIn.withProvider", { provider: provider.displayName })}
              </a>
            </Button>
          ))}
          {data?.magicLink && (
            <p className="rounded-lg border border-border bg-card p-4 text-[12.5px] text-muted-foreground leading-relaxed">
              {translate("signIn.magicLinkHint")}
            </p>
          )}
          {!data?.telegram && (data?.oidc ?? []).length === 0 && (
            <p className="rounded-lg border border-border bg-card p-4 text-[12.5px] leading-relaxed">
              {translate("signIn.noMethods")}
            </p>
          )}
        </div>
      )}
    </main>
  );
}

/**
 * The Telegram Login Widget.
 *
 * Telegram serves the widget as a script that injects its own iframe and calls a
 * global callback, so it cannot be a React component — it is mounted into a
 * container and the callback posts the signed payload to the API.
 */
function TelegramButton({ botUsername, disabled }: { botUsername: string; disabled: boolean }) {
  const translate = useTranslations("account");
  const router = useRouter();
  const [mounted, setMounted] = useState(false);

  useEffect(() => {
    const container = document.getElementById("telegram-login");
    if (!container || container.childElementCount > 0) {
      return;
    }

    // The callback is a global because that is the only interface the widget
    // offers. It is removed on unmount so a remounted screen cannot end up with
    // two live handlers.
    (window as unknown as Record<string, unknown>).onTelegramAuth = (payload: unknown) => {
      apiFetch("/v1/account/auth/telegram", {
        body: JSON.stringify(payload),
        method: "POST",
      })
        .then(() => router.replace("/account"))
        .catch((signInError: ApiError) => toast.error(signInError.message));
    };

    const script = document.createElement("script");
    script.async = true;
    script.src = "https://telegram.org/js/telegram-widget.js?22";
    script.dataset.telegramLogin = botUsername;
    script.dataset.size = "large";
    script.dataset.radius = "9";
    script.dataset.onauth = "onTelegramAuth(user)";
    script.dataset.requestAccess = "write";
    container.appendChild(script);
    setMounted(true);

    return () => {
      delete (window as unknown as Record<string, unknown>).onTelegramAuth;
    };
  }, [botUsername, router]);

  return (
    <div className="space-y-2">
      <div aria-busy={disabled} className="flex justify-center" id="telegram-login" />
      {!mounted && (
        <p className="flex items-center justify-center gap-2 text-[12.5px] text-muted-foreground">
          <MessageCircle aria-hidden className="size-4" />
          {translate("signIn.telegramHint")}
        </p>
      )}
    </div>
  );
}

/** Reads the initData a Telegram Mini App exposes, or null outside one. */
function readMiniAppInitData(): string | null {
  const telegram = (window as unknown as { Telegram?: { WebApp?: { initData?: string } } })
    .Telegram;
  const initData = telegram?.WebApp?.initData;
  return initData && initData.length > 0 ? initData : null;
}
