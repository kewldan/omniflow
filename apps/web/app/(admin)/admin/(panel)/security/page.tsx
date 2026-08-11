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
import { KeyRound, Monitor, ShieldCheck, TriangleAlert } from "lucide-react";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import { useForm } from "react-hook-form";
import useSWR from "swr";
import { z } from "zod";

import { StateNotice } from "@/components/admin/state-notice";
import { ApiError, apiFetch, fetcher } from "@/lib/api";
import { useSession } from "@/lib/session";
import { useUnsavedChanges } from "@/lib/use-unsaved-changes";

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

      <TwoFactorCard onChanged={refresh} enabled={Boolean(session?.account.totpEnabled)} />
      <PasswordCard />
      <SessionsCard />
    </div>
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
            {/*
              The secret is shown as text as well as in the URI, because an
              authenticator on the same device cannot scan this screen.
            */}
            <code className="block overflow-x-auto rounded-md bg-secondary p-3 font-mono text-[12px]">
              {enrolment.secret}
            </code>
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
