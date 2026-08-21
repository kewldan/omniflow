"use client";

import { Button } from "@omniflow/ui/button";
import { Skeleton } from "@omniflow/ui/skeleton";
import { toast } from "@omniflow/ui/toast";
import { MessageCircle, Send } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { useEffect, useRef, useState } from "react";
import useSWR from "swr";

import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { safeNext } from "@/lib/sign-in-path";

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
 *
 * The widget is not trusted to succeed. Telegram refuses to render it unless the
 * bot's configured domain matches the host it is loaded from, and it reports
 * that refusal by drawing its own error inside its own iframe — text this page
 * cannot read and did not write. The bot route is therefore rendered as a real
 * control rather than as a sentence describing one, so the screen always leaves
 * at least one way in even when the widget shows something we cannot see.
 */
export default function SignInPage() {
  const translate = useTranslations("account");
  const search = useSearchParams();
  const router = useRouter();
  const { data, isLoading } = useSWR<SignInMethods, ApiError>("/v1/account/auth/methods", fetcher);
  const [busy, setBusy] = useState(false);
  const [widgetFailed, setWidgetFailed] = useState(false);
  const [miniAppFailed, setMiniAppFailed] = useState(false);
  // One attempt per page. The effect below used to re-run whenever `busy`
  // dropped back to false, which on a refused sign-in meant an unbounded loop
  // of POSTs against the same initData; the ref makes the first attempt the
  // only one, and a failure falls through to the other sign-in methods.
  const miniAppAttempted = useRef(false);

  const reason = search.get("error");
  // Where to go once signed in. The shell and the re-authentication screens
  // send the customer here with the path they were on; an absent or unsafe
  // value lands on the dashboard.
  const next = safeNext(search.get("next"));

  // Opened inside Telegram, the surrounding client has already signed the
  // customer's identity, so the widget is unnecessary and this signs in
  // immediately. Outside Telegram the global is absent and nothing happens.
  useEffect(() => {
    const initData = readMiniAppInitData();
    if (!initData || miniAppAttempted.current) {
      return;
    }
    miniAppAttempted.current = true;
    setBusy(true);
    apiFetch("/v1/account/auth/telegram/miniapp", {
      body: JSON.stringify({ initData }),
      method: "POST",
    })
      .then(() => router.replace(next))
      .catch(() => {
        setMiniAppFailed(true);
        setBusy(false);
      });
  }, [next, router]);

  const botRoute = data?.magicLink && data.telegramBot ? data.telegramBot : undefined;

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

      {/* A Mini App sign-in that was refused is said once, and the ordinary
          methods below remain: the customer is inside Telegram, so the bot
          route in particular is one tap away. */}
      {miniAppFailed && (
        <p
          className="rounded-lg border border-destructive/40 bg-destructive/10 px-4 py-3 text-[13px]"
          role="alert"
        >
          {translate("signIn.errors.miniapp_failed")}
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
            <TelegramButton
              botUsername={data.telegramBot}
              disabled={busy}
              next={next}
              onFailed={() => setWidgetFailed(true)}
            />
          )}

          {widgetFailed && (
            <p
              className="rounded-lg border border-warning/40 bg-warning/10 px-4 py-3 text-[12.5px] leading-relaxed"
              role="alert"
            >
              {translate("signIn.telegramUnavailable")}
            </p>
          )}

          {/* The provider round trip carries `next` in the sealed flow cookie,
              so the callback lands where the customer was rather than on the
              dashboard. */}
          {(data?.oidc ?? []).map((provider) => (
            <Button asChild className="w-full" key={provider.slug} size="lg" variant="outline">
              <a
                href={`/v1/account/auth/oidc/${provider.slug}/start${
                  next === "/account" ? "" : `?next=${encodeURIComponent(next)}`
                }`}
              >
                {translate("signIn.withProvider", { provider: provider.displayName })}
              </a>
            </Button>
          ))}

          {/* The bot route as a control. `?start=login` opens the chat with the
              command already staged, so the customer taps twice rather than
              typing a command they have to be told about. */}
          {botRoute && (
            <div className="space-y-2">
              <Button asChild className="w-full" size="lg" variant="outline">
                <a
                  href={`https://t.me/${botRoute}?start=login`}
                  rel="noreferrer noopener"
                  target="_blank"
                >
                  <Send aria-hidden className="size-4" />
                  {translate("signIn.magicLinkAction")}
                </a>
              </Button>
              <p className="text-[12.5px] text-muted-foreground leading-relaxed">
                {translate("signIn.magicLinkHint")}
              </p>
            </div>
          )}

          {/* Magic link without a known bot name is the one case that still has
              to be described rather than offered. */}
          {data?.magicLink && !botRoute && (
            <p className="rounded-lg border border-border bg-card p-4 text-[12.5px] text-muted-foreground leading-relaxed">
              {translate("signIn.magicLinkHint")}
            </p>
          )}

          {!data?.telegram && (data?.oidc ?? []).length === 0 && !data?.magicLink && (
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
 *
 * `onFailed` covers the two ways this ends badly. The script itself can fail to
 * load, which fires an error we can hear; and the script can load against a host
 * the bot has not been bound to, which it reports only inside its own frame.
 * Nothing here can read that frame, so the second case is inferred from silence:
 * a widget that has not produced its iframe within a few seconds is treated as
 * one that will not.
 */
function TelegramButton({
  botUsername,
  disabled,
  next,
  onFailed,
}: {
  botUsername: string;
  disabled: boolean;
  next: string;
  onFailed: () => void;
}) {
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
    // The payload is posted exactly as the widget hands it over — `id` and
    // `auth_date` are numbers — because the signature covers the fields as
    // Telegram serialised them.
    (window as unknown as Record<string, unknown>).onTelegramAuth = (payload: unknown) => {
      apiFetch("/v1/account/auth/telegram", {
        body: JSON.stringify(payload),
        method: "POST",
      })
        .then(() => router.replace(next))
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
    script.addEventListener("error", onFailed);
    container.appendChild(script);
    setMounted(true);

    const settled = window.setTimeout(() => {
      if (!container.querySelector("iframe")) {
        onFailed();
      }
    }, 5000);

    return () => {
      window.clearTimeout(settled);
      script.removeEventListener("error", onFailed);
      delete (window as unknown as Record<string, unknown>).onTelegramAuth;
    };
  }, [botUsername, next, onFailed, router]);

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
