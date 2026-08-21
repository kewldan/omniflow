"use client";

import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { toast } from "@omniflow/ui/toast";
import { Globe, Monitor, Smartphone } from "lucide-react";
import { useRouter } from "next/navigation";
import { useFormatter, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { ReauthNotice, useReauthentication } from "@/components/account/reauth";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { useAccount } from "@/lib/account-session";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";

type Session = {
  id: string;
  current: boolean;
  authMethod: "telegram" | "magic_link" | "oidc";
  authProvider?: string;
  ip?: string;
  userAgent?: string;
  createdAt: string;
  lastSeenAt: string;
};

type SecurityEvent = {
  id: string;
  event: string;
  ip?: string;
  occurredAt: string;
};

type SignInMethod = { id: string; provider: string; label: string; removable: boolean };

/**
 * The security screen.
 *
 * It exists to answer one question — is anybody else in my account — and to give
 * the customer the controls to act on the answer without contacting support.
 */
export default function SecurityPage() {
  const translate = useTranslations("account");
  return (
    <div className="animate-step-in space-y-5">
      <SectionLabel>{translate("security.sessions")}</SectionLabel>
      <Sessions />
      <SectionLabel>{translate("security.methods")}</SectionLabel>
      <SignInMethods />
      <SectionLabel>{translate("security.events")}</SectionLabel>
      <Events />
    </div>
  );
}

function sessionIcon(session: Session) {
  if (session.authMethod === "telegram") {
    return Smartphone;
  }
  return session.authMethod === "oidc" ? Globe : Monitor;
}

function Sessions() {
  const translate = useTranslations("account");
  const format = useFormatter();
  const router = useRouter();
  const { data, error, isLoading, mutate } = useSWR<{ items: Session[] }, ApiError>(
    "/v1/account/sessions",
    fetcher,
  );
  const [busy, setBusy] = useState(false);
  const [confirmAll, setConfirmAll] = useState(false);

  async function revoke(id: string) {
    setBusy(true);
    try {
      await apiFetch(`/v1/account/sessions/${id}`, { method: "DELETE" });
      await mutate();
      toast.success(translate("security.sessionEnded"));
    } catch (revokeError) {
      toast.error((revokeError as ApiError).message);
    } finally {
      setBusy(false);
    }
  }

  async function signOutEverywhere() {
    setBusy(true);
    try {
      // keepCurrent is false: this control exists for a suspected compromise,
      // and sparing the session that pressed it would leave the attacker in if
      // they were the one holding it.
      await apiFetch("/v1/account/auth/logout-all", {
        body: JSON.stringify({ keepCurrent: false }),
        method: "POST",
      });
      router.push("/account/sign-in");
    } catch (signOutError) {
      toast.error((signOutError as ApiError).message);
      setBusy(false);
      setConfirmAll(false);
    }
  }

  if (isLoading) {
    return <ListSkeleton rows={2} />;
  }
  if (error) {
    return (
      <AccountNotice
        description={translate("states.errorDescription")}
        title={translate("states.error")}
        variant="danger"
      />
    );
  }

  return (
    <div className="space-y-3">
      <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
        {(data?.items ?? []).map((session) => {
          const Icon = sessionIcon(session);
          return (
            <li className="flex items-center gap-3 px-4 py-3.5" key={session.id}>
              <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted">
                <Icon aria-hidden className="size-[15px] text-muted-foreground" />
              </span>
              <div className="min-w-0 flex-1">
                <p className="truncate font-medium text-[14px]">
                  {translate(`profile.method.${session.authMethod}`, {
                    provider: session.authProvider ?? "",
                  })}
                </p>
                <p className="mt-0.5 truncate font-mono text-[11px] text-subtle-foreground">
                  {[session.ip, format.relativeTime(new Date(session.lastSeenAt))]
                    .filter(Boolean)
                    .join(" · ")}
                </p>
              </div>
              {session.current ? (
                <span className="font-mono text-[11px] text-subtle-foreground">
                  {translate("security.current")}
                </span>
              ) : (
                <Button
                  disabled={busy}
                  onClick={() => revoke(session.id)}
                  size="sm"
                  variant="ghost"
                >
                  {translate("security.end")}
                </Button>
              )}
            </li>
          );
        })}
      </ul>

      <Button
        className="w-full text-destructive"
        disabled={busy}
        onClick={() => setConfirmAll(true)}
        size="lg"
        variant="outline"
      >
        {translate("security.endAll")}
      </Button>

      <ConfirmDialog
        cancelLabel={translate("actions.cancel")}
        confirmLabel={translate("security.endAll")}
        description={translate("security.endAllDescription")}
        destructive
        onConfirm={signOutEverywhere}
        onOpenChange={setConfirmAll}
        open={confirmAll}
        pending={busy}
        title={translate("security.endAll")}
      />
    </div>
  );
}

/**
 * The linked sign-in methods.
 *
 * The last one cannot be removed, and the panel disables the control rather than
 * letting the request fail — the reason is worth showing before the attempt, not
 * after it.
 */
function SignInMethods() {
  const translate = useTranslations("account");
  const { session } = useAccount();
  const { data, error, isLoading, mutate } = useSWR<{ items: SignInMethod[] }, ApiError>(
    "/v1/account/sign-in-methods",
    fetcher,
  );
  const [pending, setPending] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const { redirectIfRequired } = useReauthentication();

  async function unlink(id: string) {
    setBusy(true);
    try {
      await apiFetch(`/v1/account/sign-in-methods/${id}`, { method: "DELETE" });
      await mutate();
      toast.success(translate("security.methodRemoved"));
    } catch (unlinkError) {
      // A stale session is sent to sign in again and comes back here; every
      // other refusal is reported in place.
      if (!redirectIfRequired(unlinkError)) {
        toast.error((unlinkError as ApiError).message);
      }
    } finally {
      setBusy(false);
      setPending(null);
    }
  }

  if (isLoading) {
    return <ListSkeleton rows={1} />;
  }
  if (error) {
    return null;
  }

  const needsReauth = session?.session.reauthenticationRequired ?? false;
  return (
    <div className="space-y-2">
      {needsReauth && <ReauthNotice />}
      <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
        {(data?.items ?? []).map((method) => (
          <li className="flex items-center gap-3 px-4 py-3.5" key={method.id}>
            <span className="flex-1 font-medium text-[14px]">{method.label}</span>
            <Button
              disabled={busy || !method.removable || needsReauth}
              onClick={() => setPending(method.id)}
              size="sm"
              variant="ghost"
            >
              {translate("security.unlink")}
            </Button>
          </li>
        ))}
      </ul>
      <p className="px-1 font-mono text-[11px] text-subtle-foreground">
        {translate("security.lastMethodHint")}
      </p>

      <ConfirmDialog
        cancelLabel={translate("actions.cancel")}
        confirmLabel={translate("security.unlink")}
        description={translate("security.unlinkDescription")}
        destructive
        onConfirm={() => pending && unlink(pending)}
        onOpenChange={(open) => !open && setPending(null)}
        open={pending !== null}
        pending={busy}
        title={translate("security.unlink")}
      />
    </div>
  );
}

/** The customer's own account history. */
function Events() {
  const translate = useTranslations("account");
  const format = useFormatter();
  const { data, isLoading } = useSWR<{ items: SecurityEvent[] }, ApiError>(
    "/v1/account/security-events",
    fetcher,
  );

  if (isLoading) {
    return <ListSkeleton rows={2} />;
  }
  const events = data?.items ?? [];
  if (events.length === 0) {
    return (
      <AccountNotice
        description={translate("security.eventsEmptyDescription")}
        title={translate("security.eventsEmpty")}
      />
    );
  }

  return (
    <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
      {events.map((event) => (
        <li className="flex items-center justify-between gap-3 px-4 py-3.5" key={event.id}>
          <span className="font-medium text-[13.5px]">
            {translate(`security.event.${event.event}`)}
          </span>
          <span className="shrink-0 font-mono text-[11px] text-subtle-foreground">
            {format.dateTime(new Date(event.occurredAt), {
              day: "numeric",
              hour: "2-digit",
              minute: "2-digit",
              month: "short",
            })}
          </span>
        </li>
      ))}
    </ul>
  );
}
