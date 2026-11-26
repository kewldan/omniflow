"use client";

import { Button } from "@omniflow/ui/button";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useEffect, useRef, useState } from "react";
import useSWR from "swr";

import { ProviderMark } from "@/components/account/provider-mark";
import { useReauthentication } from "@/components/account/reauth";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";

type AvailableMethods = {
  telegram: boolean;
  telegramBot?: string;
  oidc: { slug: string; displayName: string; icon?: string }[];
};

/**
 * Adds another way into the account: Telegram through the Login Widget, or an
 * OpenID Connect provider through its own consent screen.
 *
 * Only what the installation offers and the customer has not linked yet is
 * shown. Adding a method needs a recent sign-in, like removing one, so a stale
 * session is sent to sign in again before the provider is even opened.
 */
export function AddSignInMethod({
  linkedProviders,
  needsReauth,
  onLinked,
}: {
  linkedProviders: string[];
  needsReauth: boolean;
  onLinked: () => void;
}) {
  const translate = useTranslations("account.security");
  const { data } = useSWR<AvailableMethods, ApiError>("/v1/account/auth/methods", fetcher);
  const { href: reauthHref } = useReauthentication();
  if (!data) {
    return null;
  }

  const hasTelegram = linkedProviders.includes("telegram");
  const providers = data.oidc.filter(
    (provider) => !linkedProviders.includes(`oidc:${provider.slug}`),
  );
  const offerTelegram = data.telegram && Boolean(data.telegramBot) && !hasTelegram;
  if (!offerTelegram && providers.length === 0) {
    return null;
  }

  return (
    <div className="space-y-3 rounded-xl border border-border bg-card px-4 py-3.5">
      <div>
        <div className="font-medium text-[14px]">{translate("addMethod")}</div>
        <p className="text-[12.5px] text-muted-foreground">{translate("addMethodHint")}</p>
      </div>
      {needsReauth ? (
        <Button asChild size="sm" variant="secondary">
          <a href={reauthHref}>{translate("addMethodReauth")}</a>
        </Button>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          {offerTelegram && data.telegramBot && (
            <TelegramLinkButton botUsername={data.telegramBot} onLinked={onLinked} />
          )}
          {providers.map((provider) => (
            <Button asChild key={provider.slug} size="sm" variant="secondary">
              <a
                href={`/v1/account/sign-in-methods/oidc/${encodeURIComponent(provider.slug)}/start?next=${encodeURIComponent("/account/security")}`}
              >
                <ProviderMark className="size-4" provider={provider.icon || provider.slug} />
                {translate("linkProvider", { provider: provider.displayName })}
              </a>
            </Button>
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * The Telegram Login Widget, posting to the link route rather than sign-in.
 *
 * The widget can only call a global, so its callback is registered on mount and
 * removed on unmount. The payload goes to the API exactly as the widget hands it
 * over, because the signature covers the fields as Telegram serialised them.
 */
function TelegramLinkButton({
  botUsername,
  onLinked,
}: {
  botUsername: string;
  onLinked: () => void;
}) {
  const translate = useTranslations("account.security");
  const { redirectIfRequired } = useReauthentication();
  const container = useRef<HTMLDivElement>(null);
  const [failed, setFailed] = useState(false);
  const latest = useRef({ onLinked, redirectIfRequired, translate });
  useEffect(() => {
    latest.current = { onLinked, redirectIfRequired, translate };
  });

  useEffect(() => {
    const target = container.current;
    if (!target || target.childElementCount > 0) {
      return;
    }
    const global = window as unknown as Record<string, unknown>;
    global.onTelegramLink = (payload: unknown) => {
      apiFetch("/v1/account/sign-in-methods/telegram", {
        body: JSON.stringify(payload),
        method: "POST",
      })
        .then(() => {
          toast.success(latest.current.translate("methodLinked"));
          latest.current.onLinked();
        })
        .catch((linkError: ApiError) => {
          if (latest.current.redirectIfRequired(linkError)) {
            return;
          }
          const key = `linkErrors.${linkError.code ?? ""}`;
          const describe = latest.current.translate;
          toast.error(describe.has(key) ? describe(key) : linkError.message);
        });
    };

    const script = document.createElement("script");
    script.async = true;
    script.src = "https://telegram.org/js/telegram-widget.js?22";
    script.dataset.telegramLogin = botUsername;
    script.dataset.size = "medium";
    script.dataset.radius = "8";
    script.dataset.onauth = "onTelegramLink(user)";
    script.dataset.requestAccess = "write";
    const fail = () => setFailed(true);
    script.addEventListener("error", fail);
    target.appendChild(script);
    const settled = window.setTimeout(() => {
      if (!target.querySelector("iframe")) {
        setFailed(true);
      }
    }, 5000);

    return () => {
      window.clearTimeout(settled);
      script.removeEventListener("error", fail);
      delete global.onTelegramLink;
    };
  }, [botUsername]);

  return (
    <div className="flex items-center gap-2">
      <div ref={container} />
      {failed && (
        <span className="text-[12px] text-muted-foreground">
          {translate("telegramWidgetUnavailable")}
        </span>
      )}
    </div>
  );
}
