"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { Alert, AlertDescription, AlertTitle } from "@omniflow/ui/alert";
import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@omniflow/ui/card";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { toast } from "@omniflow/ui/toast";
import { Fingerprint, KeyRound, Monitor, ShieldCheck, TriangleAlert } from "lucide-react";
import { useTranslations } from "next-intl";
import { useEffect, useId, useState } from "react";
import { useForm } from "react-hook-form";
import useSWR from "swr";
import { z } from "zod";

import { PasswordStrength } from "@/components/admin/password-strength";
import { StateNotice } from "@/components/admin/state-notice";
import { QrCode } from "@/components/qr-code";
import { ApiError, apiFetch, fetcher } from "@/lib/api";
import { passkeyDismissed, passkeysSupported, registerPasskey } from "@/lib/passkey";
import { useSession } from "@/lib/session";
import { useUnsavedChanges } from "@/lib/use-unsaved-changes";

type Passkey = {
  id: string;
  label: string;
  createdAt: string;
  lastUsedAt?: string;
  discoverable: boolean;
};

type AdminSession = {
  id: string;
  current: boolean;
  ip?: string;
  userAgent?: string;
  lastSeenAt: string;
  expiresAt: string;
  methods: string[];
};

const passwordSchema = z
  .object({
    confirmPassword: z.string(),
    currentPassword: z.string().min(1),
    newPassword: z.string().min(12).max(256),
  })
  .refine((values) => values.newPassword === values.confirmPassword, {
    path: ["confirmPassword"],
  });

export default function SecurityPage() {
  const translate = useTranslations("admin.security");
  const { session, refresh } = useSession();

  return (
    <div className="flex flex-col gap-5">
      <header className="flex flex-col gap-1">
        <h1 className="font-bold text-2xl tracking-tight">{translate("title")}</h1>
        <p className="max-w-2xl text-muted-foreground text-sm">{translate("description")}</p>
      </header>

      <PasskeysCard />
      <TwoFactorCard onChanged={refresh} enabled={Boolean(session?.account.totpEnabled)} />
      <PasswordCard />
      <SessionsCard />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Passkeys
// ---------------------------------------------------------------------------

/**
 * Registered passkeys, and the controls to add and remove them.
 *
 * A passkey signs in on its own, so this card sits above the second factor: it
 * is the route that replaces both steps rather than an addition to them. The
 * password stays underneath as the way back in when every key is lost, which is
 * why removing the last one is allowed at all.
 */
function PasskeysCard() {
  const translate = useTranslations("admin.security.passkeys");
  const { data, error, isLoading, mutate } = useSWR<
    { items: Passkey[]; available: boolean },
    ApiError
  >("/v1/panel/auth/passkeys", fetcher);

  const [browserSupported, setBrowserSupported] = useState(false);
  const [label, setLabel] = useState("");
  const [busy, setBusy] = useState(false);
  const [renaming, setRenaming] = useState<{ id: string; label: string } | null>(null);
  const [removing, setRemoving] = useState<Passkey | null>(null);
  const labelId = useId();

  // Read in an effect: `window` does not exist during the server render, and
  // deciding during render would make the markup disagree with itself.
  useEffect(() => {
    setBrowserSupported(passkeysSupported());
  }, []);

  const keys = data?.items ?? [];
  const configured = data?.available ?? false;

  async function register() {
    setBusy(true);
    try {
      await registerPasskey(label.trim() || translate("defaultLabel"));
      setLabel("");
      await mutate();
      toast.success(translate("registered"));
    } catch (registerError) {
      // Cancelling the browser's dialog is a choice, not a failure worth a
      // red toast.
      if (passkeyDismissed(registerError)) {
        return;
      }
      toast.error(
        registerError instanceof ApiError && registerError.code === "passkey_unverified"
          ? translate("errors.unverified")
          : translate("errors.register"),
      );
    } finally {
      setBusy(false);
    }
  }

  async function rename() {
    if (!renaming) {
      return;
    }
    setBusy(true);
    try {
      await apiFetch(`/v1/panel/auth/passkeys/${renaming.id}`, {
        body: JSON.stringify({ label: renaming.label.trim() }),
        method: "PATCH",
      });
      setRenaming(null);
      await mutate();
      toast.success(translate("renamed"));
    } catch {
      toast.error(translate("errors.rename"));
    } finally {
      setBusy(false);
    }
  }

  async function remove(id: string) {
    setBusy(true);
    try {
      await apiFetch(`/v1/panel/auth/passkeys/${id}`, { method: "DELETE" });
      setRemoving(null);
      await mutate();
      toast.success(translate("removed"));
    } catch {
      toast.error(translate("errors.remove"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader className="flex-row items-center">
        <div className="flex flex-col gap-1.5">
          <CardTitle>{translate("title")}</CardTitle>
          <CardDescription>{translate("description")}</CardDescription>
        </div>
        <CardAction>
          <Badge variant={keys.length > 0 ? "success" : "outline"}>
            {translate("count", { count: keys.length })}
          </Badge>
        </CardAction>
      </CardHeader>

      <CardContent className="flex flex-col gap-4">
        {!configured && !isLoading && (
          // The installation has no public URL, so there is no origin to bind a
          // credential to. Said here rather than left for the button to fail on.
          <Alert variant="warning">
            <TriangleAlert />
            <AlertTitle>{translate("unavailable")}</AlertTitle>
            <AlertDescription>{translate("unavailableDescription")}</AlertDescription>
          </Alert>
        )}
        {configured && !browserSupported && (
          <Alert variant="warning">
            <TriangleAlert />
            <AlertTitle>{translate("unsupportedBrowser")}</AlertTitle>
            <AlertDescription>{translate("unsupportedBrowserDescription")}</AlertDescription>
          </Alert>
        )}

        {isLoading ? (
          <div aria-busy="true" className="flex flex-col gap-2">
            <Skeleton className="h-12 w-full" />
          </div>
        ) : error ? (
          <StateNotice
            action={
              <Button onClick={() => mutate()} size="sm" variant="outline">
                {translate("retry")}
              </Button>
            }
            title={translate("errors.load")}
            variant="danger"
          />
        ) : keys.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("empty")}</p>
        ) : (
          <ul className="flex flex-col divide-y divide-border">
            {keys.map((item) => (
              <li className="flex items-center gap-3 py-3" key={item.id}>
                <Fingerprint aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
                <div className="min-w-0 flex-1">
                  {renaming?.id === item.id ? (
                    <Input
                      aria-label={translate("labelField")}
                      className="max-w-64"
                      maxLength={60}
                      onChange={(event) => setRenaming({ id: item.id, label: event.target.value })}
                      value={renaming.label}
                    />
                  ) : (
                    <p className="truncate text-sm">{item.label}</p>
                  )}
                  <p className="font-mono text-[11px] text-subtle-foreground" data-numeric>
                    {translate("added", { date: new Date(item.createdAt).toLocaleDateString() })}
                    {" · "}
                    {item.lastUsedAt
                      ? translate("lastUsed", {
                          date: new Date(item.lastUsedAt).toLocaleDateString(),
                        })
                      : translate("neverUsed")}
                  </p>
                </div>
                {renaming?.id === item.id ? (
                  <>
                    <Button
                      disabled={busy || renaming.label.trim().length === 0}
                      onClick={rename}
                      size="sm"
                    >
                      {translate("save")}
                    </Button>
                    <Button onClick={() => setRenaming(null)} size="sm" variant="ghost">
                      {translate("cancel")}
                    </Button>
                  </>
                ) : (
                  <>
                    <Button
                      disabled={busy}
                      onClick={() => setRenaming({ id: item.id, label: item.label })}
                      size="sm"
                      variant="ghost"
                    >
                      {translate("rename")}
                    </Button>
                    <Button
                      disabled={busy}
                      onClick={() => setRemoving(item)}
                      size="sm"
                      variant="ghost"
                    >
                      {translate("remove")}
                    </Button>
                  </>
                )}
              </li>
            ))}
          </ul>
        )}

        {configured && browserSupported && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={labelId}>{translate("labelField")}</Label>
            <div className="flex flex-wrap items-center gap-2">
              <Input
                className="max-w-64"
                id={labelId}
                maxLength={60}
                onChange={(event) => setLabel(event.target.value)}
                placeholder={translate("labelPlaceholder")}
                value={label}
              />
              <Button disabled={busy} onClick={register}>
                <Fingerprint />
                {translate("add")}
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">{translate("labelHint")}</p>
          </div>
        )}
      </CardContent>

      <ConfirmDialog
        cancelLabel={translate("cancel")}
        confirmLabel={translate("remove")}
        description={translate("removeWarning", { label: removing?.label ?? "" })}
        destructive
        onConfirm={() => removing && remove(removing.id)}
        onOpenChange={(open) => {
          if (!open) {
            setRemoving(null);
          }
        }}
        open={Boolean(removing)}
        pending={busy}
        title={translate("removeTitle")}
      />
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Two-factor
// ---------------------------------------------------------------------------

function TwoFactorCard({ enabled, onChanged }: { enabled: boolean; onChanged: () => void }) {
  const translate = useTranslations("admin.security.twoFactor");
  const { session } = useSession();
  const [enrolment, setEnrolment] = useState<{ secret: string; uri: string } | null>(null);
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [disabling, setDisabling] = useState(false);
  const [password, setPassword] = useState("");
  const codeId = useId();

  const remaining = session?.remainingRecoveryCodes ?? 0;

  async function begin() {
    setBusy(true);
    try {
      setEnrolment(await apiFetch("/v1/panel/auth/totp", { method: "POST" }));
    } catch {
      toast.error(translate("errors.start"));
    } finally {
      setBusy(false);
    }
  }

  async function confirm() {
    setBusy(true);
    try {
      const result = await apiFetch<{ recoveryCodes: string[] }>("/v1/panel/auth/totp/confirm", {
        body: JSON.stringify({ code }),
        method: "POST",
      });
      setRecoveryCodes(result.recoveryCodes);
      setEnrolment(null);
      setCode("");
      onChanged();
      toast.success(translate("enabled"));
    } catch (error) {
      toast.error(
        error instanceof ApiError && error.code === "invalid_code"
          ? translate("errors.invalidCode")
          : translate("errors.confirm"),
      );
    } finally {
      setBusy(false);
    }
  }

  async function disable() {
    setBusy(true);
    try {
      await apiFetch("/v1/panel/auth/totp", {
        body: JSON.stringify({ password }),
        method: "DELETE",
      });
      setDisabling(false);
      setPassword("");
      onChanged();
      toast.success(translate("disabled"));
    } catch {
      toast.error(translate("errors.disable"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader className="flex-row items-center">
        <div className="flex flex-col gap-1.5">
          <CardTitle>{translate("title")}</CardTitle>
          <CardDescription>{translate("description")}</CardDescription>
        </div>
        <CardAction>
          <Badge variant={enabled ? "success" : "warning"}>
            {enabled ? translate("on") : translate("off")}
          </Badge>
        </CardAction>
      </CardHeader>

      <CardContent className="flex flex-col gap-4">
        {!enabled && !enrolment && (
          <Alert variant="warning">
            <TriangleAlert />
            <AlertTitle>{translate("recommendTitle")}</AlertTitle>
            <AlertDescription>{translate("recommendDescription")}</AlertDescription>
          </Alert>
        )}

        {enrolment && (
          <div className="flex flex-col gap-3">
            <p className="text-muted-foreground text-sm">{translate("scanHint")}</p>
            <div className="flex flex-wrap items-start gap-4">
              {/* The URI has always been returned and never drawn, so enrolling
                  meant typing a thirty-two character secret into a phone. It is
                  encoded in the browser: an otpauth URI carries the secret
                  itself, and fetching it as an image would put that into a
                  request line and a cache. */}
              <div className="flex size-[168px] shrink-0 items-center justify-center overflow-hidden rounded-lg border border-border bg-white p-2">
                <QrCode alt={translate("qrAlt")} value={enrolment.uri} />
              </div>
              <div className="flex min-w-[16rem] flex-1 flex-col gap-1.5">
                <span className="text-muted-foreground text-xs">{translate("secretLabel")}</span>
                {/*
                  The secret is shown as text as well as in the QR, because an
                  authenticator on the same device cannot scan this screen.
                */}
                <code className="block overflow-x-auto rounded-md bg-secondary p-3 font-mono text-[12px]">
                  {enrolment.secret}
                </code>
              </div>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={codeId}>{translate("codeLabel")}</Label>
              <Input
                autoComplete="one-time-code"
                className="max-w-40 font-mono"
                id={codeId}
                inputMode="numeric"
                onChange={(event) => setCode(event.target.value)}
                value={code}
              />
            </div>
          </div>
        )}

        {recoveryCodes && (
          <Alert variant="info">
            <KeyRound />
            <AlertTitle>{translate("recoveryTitle")}</AlertTitle>
            <AlertDescription>
              <p className="mb-2">{translate("recoveryDescription")}</p>
              <ul className="grid grid-cols-2 gap-1 font-mono text-[12px]">
                {recoveryCodes.map((recoveryCode) => (
                  <li key={recoveryCode}>{recoveryCode}</li>
                ))}
              </ul>
            </AlertDescription>
          </Alert>
        )}

        {enabled && !recoveryCodes && (
          <p className="text-muted-foreground text-sm">
            {translate("remaining", { count: remaining })}
          </p>
        )}
      </CardContent>

      <CardFooter>
        {enrolment ? (
          <>
            <Button disabled={busy || code.length < 6} onClick={confirm}>
              {translate("confirm")}
            </Button>
            <Button onClick={() => setEnrolment(null)} variant="ghost">
              {translate("cancel")}
            </Button>
          </>
        ) : enabled ? (
          <Button onClick={() => setDisabling(true)} variant="outline">
            {translate("disable")}
          </Button>
        ) : (
          <Button disabled={busy} onClick={begin}>
            <ShieldCheck />
            {translate("enable")}
          </Button>
        )}
      </CardFooter>

      <ConfirmDialog
        cancelLabel={translate("cancel")}
        confirmLabel={translate("disable")}
        description={
          <div className="flex flex-col gap-3">
            <span>{translate("disableWarning")}</span>
            <Input
              autoComplete="current-password"
              onChange={(event) => setPassword(event.target.value)}
              placeholder={translate("passwordPlaceholder")}
              type="password"
              value={password}
            />
          </div>
        }
        destructive
        onConfirm={disable}
        onOpenChange={(open) => {
          setDisabling(open);
          if (!open) {
            setPassword("");
          }
        }}
        open={disabling}
        pending={busy || password.length === 0}
        title={translate("disableTitle")}
      />
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Password
// ---------------------------------------------------------------------------

function PasswordCard() {
  const translate = useTranslations("admin.security.password");
  const guard = useTranslations("admin");
  const currentId = useId();
  const nextId = useId();
  const confirmId = useId();

  const form = useForm<z.infer<typeof passwordSchema>>({
    defaultValues: { confirmPassword: "", currentPassword: "", newPassword: "" },
    resolver: zodResolver(passwordSchema),
  });

  // Navigating away mid-change loses the typed passwords silently, so the
  // attempt is confirmed rather than absorbed.
  useUnsavedChanges(
    form.formState.isDirty && !form.formState.isSubmitSuccessful,
    guard("states.unsavedChanges"),
  );

  async function submit(values: z.infer<typeof passwordSchema>) {
    try {
      await apiFetch("/v1/panel/auth/password", {
        body: JSON.stringify({
          currentPassword: values.currentPassword,
          newPassword: values.newPassword,
        }),
        method: "POST",
      });
      form.reset();
      toast.success(translate("changed"));
    } catch (error) {
      toast.error(
        error instanceof ApiError && error.code === "invalid_credentials"
          ? translate("errors.wrongCurrent")
          : translate("errors.generic"),
      );
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <form noValidate onSubmit={form.handleSubmit(submit)}>
        <CardContent className="flex max-w-sm flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={currentId}>{translate("current")}</Label>
            <Input
              autoComplete="current-password"
              id={currentId}
              type="password"
              {...form.register("currentPassword")}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={nextId}>{translate("next")}</Label>
            <Input
              autoComplete="new-password"
              id={nextId}
              type="password"
              {...form.register("newPassword")}
            />
            <p className="text-muted-foreground text-xs">{translate("hint")}</p>
            {/* Advice, never a gate. The minimum length is the API's rule and
                is enforced there; a meter that refused would be a second place
                deciding what a valid password is. */}
            <PasswordStrength password={form.watch("newPassword") ?? ""} />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={confirmId}>{translate("confirm")}</Label>
            <Input
              aria-invalid={Boolean(form.formState.errors.confirmPassword)}
              autoComplete="new-password"
              id={confirmId}
              type="password"
              {...form.register("confirmPassword")}
            />
            {form.formState.errors.confirmPassword && (
              <p className="text-destructive text-xs">{translate("errors.mismatch")}</p>
            )}
          </div>
        </CardContent>
        <CardFooter>
          <Button disabled={form.formState.isSubmitting} type="submit">
            {translate("submit")}
          </Button>
          {/* Changing the password ends every other session, so say so up front. */}
          <p className="text-muted-foreground text-xs">{translate("revokesOthers")}</p>
        </CardFooter>
      </form>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

function SessionsCard() {
  const translate = useTranslations("admin.security.sessions");
  const { data, error, isLoading, mutate } = useSWR<{ items: AdminSession[] }, ApiError>(
    "/v1/panel/auth/sessions",
    fetcher,
  );
  const [busy, setBusy] = useState(false);

  async function revoke(id: string) {
    setBusy(true);
    try {
      await apiFetch(`/v1/panel/auth/sessions/${id}`, { method: "DELETE" });
      await mutate();
      toast.success(translate("revoked"));
    } catch {
      toast.error(translate("revokeFailed"));
    } finally {
      setBusy(false);
    }
  }

  async function revokeAll() {
    setBusy(true);
    try {
      await apiFetch("/v1/panel/auth/logout-all", { method: "POST" });
      await mutate();
      toast.success(translate("revokedAll"));
    } catch {
      toast.error(translate("revokeFailed"));
    } finally {
      setBusy(false);
    }
  }

  const sessions = data?.items ?? [];

  return (
    <Card>
      <CardHeader className="flex-row items-center">
        <div className="flex flex-col gap-1.5">
          <CardTitle>{translate("title")}</CardTitle>
          <CardDescription>{translate("description")}</CardDescription>
        </div>
        <CardAction>
          <Button
            disabled={busy || sessions.length < 2}
            onClick={revokeAll}
            size="sm"
            variant="outline"
          >
            {translate("revokeOthers")}
          </Button>
        </CardAction>
      </CardHeader>

      <CardContent>
        {isLoading ? (
          <div aria-busy="true" className="flex flex-col gap-2">
            <Skeleton className="h-12 w-full" />
            <Skeleton className="h-12 w-full" />
          </div>
        ) : error ? (
          <StateNotice
            action={
              <Button onClick={() => mutate()} size="sm" variant="outline">
                {translate("retry")}
              </Button>
            }
            title={translate("error")}
            variant="danger"
          />
        ) : (
          <ul className="flex flex-col divide-y divide-border">
            {sessions.map((item) => (
              <li className="flex items-center gap-3 py-3" key={item.id}>
                <Monitor aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm">
                    {item.userAgent || translate("unknownDevice")}
                    {item.current && (
                      <Badge className="ml-2" variant="success">
                        {translate("current")}
                      </Badge>
                    )}
                  </p>
                  <p className="font-mono text-[11px] text-subtle-foreground" data-numeric>
                    {item.ip ?? "—"} · {new Date(item.lastSeenAt).toLocaleString()}
                  </p>
                </div>
                {!item.current && (
                  <Button disabled={busy} onClick={() => revoke(item.id)} size="sm" variant="ghost">
                    {translate("revoke")}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
