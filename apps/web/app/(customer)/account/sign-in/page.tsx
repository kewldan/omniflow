"use client";

import { Button } from "@omniflow/ui/button";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { toast } from "@omniflow/ui/toast";
import { Mail, MessageCircle, Send } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { useLocale, useTranslations } from "next-intl";
import { useEffect, useRef, useState } from "react";
import useSWR from "swr";

import { useBrandName } from "@/components/brand-name";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { rememberReferralCode } from "@/lib/referral";
import { safeNext } from "@/lib/sign-in-path";
import { type MiniAppUser, miniAppUser } from "@/lib/telegram-miniapp";

type SignInMethods = {
  telegram: boolean;
  /** The bot's own @name, which is what the login widget is loaded by. */
  telegramBot?: string;
  magicLink: boolean;
  /** Sign-in by e-mailed link: a mail transport is configured. */
  email?: boolean;
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
  const serviceName = useBrandName(translate("nav.title"));
  const search = useSearchParams();
  const router = useRouter();
  const { data, isLoading } = useSWR<SignInMethods, ApiError>("/v1/account/auth/methods", fetcher);
  const [busy, setBusy] = useState(false);
  const [widgetFailed, setWidgetFailed] = useState(false);
  const [miniAppFailed, setMiniAppFailed] = useState(false);
  // Where the screen was opened: unknown until the first effect runs, then a
  // browser or a Telegram Mini App with the launch data it was handed. The
  // methods are not drawn while it is unknown, so a Mini App never flashes
  // the browser's sign-in methods before its own button.
  const [opened, setOpened] = useState<
    | { in: "pending" }
    | { in: "web" }
    | { in: "miniapp"; initData: string; user: MiniAppUser | null }
  >({ in: "pending" });

  const reason = search.get("error");
  // Where to go once signed in. The shell and the re-authentication screens
  // send the customer here with the path they were on; an absent or unsafe
  // value lands on the dashboard.
  const next = safeNext(search.get("next"));

  // An invite link lands here as `?ref=<code>`. The code is held until a
  // session exists and then attributed, the way the bot attributes
  // `/start ref_<code>`; the shell posts it on its first authenticated render.
  useEffect(() => {
    rememberReferralCode(search.get("ref"));
  }, [search]);

  // Inside Telegram the client has already signed who the customer is, so
  // the screen offers one button: sign in as that person. Outside Telegram
  // the launch data is absent and the browser's methods are shown.
  useEffect(() => {
    const initData = readMiniAppInitData();
    setOpened(initData ? { in: "miniapp", initData, user: miniAppUser(initData) } : { in: "web" });
  }, []);

  function signInWithMiniApp(initData: string) {
    setBusy(true);
    apiFetch("/v1/account/auth/telegram/miniapp", {
      body: JSON.stringify({ initData }),
      method: "POST",
    })
      .then(() => router.replace(next))
      .catch(() => {
        // A refusal is said once, and the browser's methods appear below it.
        setMiniAppFailed(true);
        setBusy(false);
      });
  }

  const botRoute = data?.magicLink && data.telegramBot ? data.telegramBot : undefined;

  return (
    <main className="mx-auto flex min-h-dvh max-w-sm flex-col justify-center gap-6 px-6 py-16">
      <header className="space-y-2">
        <p className="font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.16em]">
          {serviceName}
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
          {translate.has(`signIn.errors.${reason}`)
            ? translate(`signIn.errors.${reason}`)
            : translate("signIn.errors.generic")}
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

      {opened.in === "miniapp" && !miniAppFailed ? (
        <MiniAppSignIn
          busy={busy}
          onSignIn={() => signInWithMiniApp(opened.initData)}
          user={opened.user}
        />
      ) : isLoading || opened.in === "pending" ? (
        <div className="space-y-3">
          <Skeleton className="h-11 w-full rounded-md" />
          <Skeleton className="h-11 w-full rounded-md" />
        </div>
      ) : (
        <div className="space-y-3">
          {/* Telegram first, as a plain button: it opens the bot, which sends a
              one-time link back. It needs no domain bound to the bot, so it
              works where the widget cannot load. */}
          {botRoute && (
            <div className="space-y-2">
              <Button asChild className="w-full" size="lg">
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

          {/* The widget only where the bot route is not offered: two Telegram
              controls side by side read as two different accounts. */}
          {!botRoute && data?.telegram && data.telegramBot && (
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

          {/* Magic link without a known bot name is the one case that still has
              to be described rather than offered. */}
          {data?.magicLink && !botRoute && (
            <p className="rounded-lg border border-border bg-card p-4 text-[12.5px] text-muted-foreground leading-relaxed">
              {translate("signIn.magicLinkHint")}
            </p>
          )}

          {data?.email && <EmailSignIn />}

          {!data?.telegram &&
            (data?.oidc ?? []).length === 0 &&
            !data?.magicLink &&
            !data?.email && (
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
 * The Mini App's sign-in: one button naming the person Telegram says opened
 * it, with their picture. Pressing it sends the signed launch data, which the
 * API verifies before a session exists.
 */
function MiniAppSignIn({
  busy,
  onSignIn,
  user,
}: {
  busy: boolean;
  onSignIn: () => void;
  user: MiniAppUser | null;
}) {
  const translate = useTranslations("account");
  const [pictureFailed, setPictureFailed] = useState(false);
  const initials = (user?.name ?? "")
    .replace(/^@/, "")
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase() ?? "")
    .join("");
  return (
    <Button
      className="h-14 w-full justify-start gap-3 px-4"
      disabled={busy}
      onClick={onSignIn}
      size="lg"
    >
      {user ? (
        user.photoUrl && !pictureFailed ? (
          // biome-ignore lint/performance/noImgElement: Telegram's own picture URL, shown once on this screen
          <img
            alt=""
            className="size-9 shrink-0 rounded-full object-cover"
            height={36}
            onError={() => setPictureFailed(true)}
            // Telegram answers a picture it does not have with a 1×1 image,
            // which would draw an empty circle; initials read better.
            onLoad={(event) => {
              if (event.currentTarget.naturalWidth < 2) {
                setPictureFailed(true);
              }
            }}
            referrerPolicy="no-referrer"
            src={user.photoUrl}
            width={36}
          />
        ) : (
          <span
            aria-hidden
            className="flex size-9 shrink-0 items-center justify-center rounded-full bg-background/20 font-semibold text-sm"
          >
            {initials || <Send className="size-4" />}
          </span>
        )
      ) : (
        <Send aria-hidden className="size-4" />
      )}
      <span className="truncate text-left">
        {busy
          ? translate("signIn.miniApp.signingIn")
          : user
            ? translate("signIn.miniApp.as", { name: user.name })
            : translate("signIn.miniApp.generic")}
      </span>
    </Button>
  );
}

/**
 * Sign-in by e-mailed link.
 *
 * The API answers the same way for an address it knows and one it does not, so
 * this screen only ever says "check your inbox" — it cannot be used to learn
 * whether somebody has an account here.
 */
function EmailSignIn() {
  const translate = useTranslations("account");
  const locale = useLocale();
  const [email, setEmail] = useState("");
  const [state, setState] = useState<"idle" | "sending" | "sent">("idle");
  const [error, setError] = useState<string | null>(null);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    setState("sending");
    try {
      await apiFetch("/v1/account/auth/email", {
        body: JSON.stringify({ email: email.trim(), locale }),
        method: "POST",
      });
      setState("sent");
    } catch (failure) {
      const code = (failure as ApiError).code;
      setError(
        code === "email_invalid" || code === "rate_limited"
          ? translate(`signIn.email.errors.${code}`)
          : translate("signIn.email.errors.generic"),
      );
      setState("idle");
    }
  }

  if (state === "sent") {
    return (
      <p
        className="rounded-lg border border-border bg-card p-4 text-[12.5px] leading-relaxed"
        role="status"
      >
        {translate("signIn.email.sent", { email: email.trim() })}
      </p>
    );
  }
  return (
    <form className="space-y-2" noValidate onSubmit={submit}>
      <Label htmlFor="sign-in-email">{translate("signIn.email.label")}</Label>
      <Input
        aria-describedby={error ? "sign-in-email-error" : undefined}
        aria-invalid={error ? true : undefined}
        autoComplete="email"
        id="sign-in-email"
        inputMode="email"
        onChange={(event) => setEmail(event.target.value)}
        placeholder="name@example.com"
        type="email"
        value={email}
      />
      {error && (
        <p className="text-[12.5px] text-destructive" id="sign-in-email-error" role="alert">
          {error}
        </p>
      )}
      <Button
        className="w-full"
        disabled={state === "sending" || email.trim() === ""}
        size="lg"
        type="submit"
        variant="outline"
      >
        <Mail aria-hidden className="size-4" />
        {state === "sending" ? translate("signIn.email.sending") : translate("signIn.email.action")}
      </Button>
    </form>
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
  // The widget's callback is registered once and must not be re-registered
  // when the translator's identity changes, so the latest translator is read
  // through a ref rather than listed as a dependency.
  const translateRef = useRef(translate);
  useEffect(() => {
    translateRef.current = translate;
  }, [translate]);

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
        .catch((signInError: ApiError) => {
          // A refusal with a code the catalogue explains is shown in the
          // customer's language; anything else falls back to the problem's
          // own title.
          const key = `signIn.errors.${signInError.code ?? ""}`;
          const describe = translateRef.current;
          toast.error(describe.has(key) ? describe(key) : signInError.message);
        });
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

/**
 * Reads the initData a Telegram Mini App hands its page, or null outside one.
 *
 * `window.Telegram.WebApp` exists only once Telegram's own script has loaded,
 * and this panel does not load it. Telegram also passes the same string in
 * the launch URL's fragment as `tgWebAppData`, which is always there inside a
 * Mini App and survives the edge gate's redirect to this screen (a fragment is
 * kept across a redirect), so that is read when the global is absent.
 */
function readMiniAppInitData(): string | null {
  const telegram = (window as unknown as { Telegram?: { WebApp?: { initData?: string } } })
    .Telegram;
  const initData = telegram?.WebApp?.initData;
  if (initData && initData.length > 0) {
    return initData;
  }
  const fromFragment = new URLSearchParams(window.location.hash.replace(/^#/, "")).get(
    "tgWebAppData",
  );
  return fromFragment && fromFragment.length > 0 ? fromFragment : null;
}
