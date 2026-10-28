"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { Alert, AlertDescription } from "@omniflow/ui/alert";
import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Skeleton } from "@omniflow/ui/skeleton";
import { AlertTriangle, ShieldCheck } from "lucide-react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useLocale, useTranslations } from "next-intl";
import { useId, useState } from "react";
import { useForm } from "react-hook-form";
import useSWR from "swr";
import { z } from "zod";

import { PasswordStrength } from "@/components/admin/password-strength";
import { ApiError, apiFetch, setCsrfToken } from "@/lib/api";

// Mirrors the server policy in internal/adminauth: length is the control that
// matters, and no composition rules are imposed.
const schema = z
  .object({
    confirmPassword: z.string(),
    displayName: z.string().trim().min(1).max(80),
    password: z.string().min(12).max(256),
  })
  .refine((values) => values.password === values.confirmPassword, {
    message: "passwordMismatch",
    path: ["confirmPassword"],
  });

type Values = z.infer<typeof schema>;

type Preview = { email: string; roles: string[]; expiresAt: string };

type Accepted = { signedIn: boolean; csrfToken?: string };

/**
 * Redeeming an operator invitation.
 *
 * Outside the panel shell for the same reason sign-in is: the visitor has no
 * session yet. The token from the path goes to the API in a request body, never
 * in a query string. Success signs the new operator in and offers the security
 * page next, where two-factor and passkeys are enrolled — the installation does
 * not force either, so it is an offer, made at the moment it is most likely to
 * be taken up.
 */
export default function InvitationPage() {
  const translate = useTranslations("admin.invitation");
  const translateRole = useTranslations("admin.roles");
  const router = useRouter();
  const locale = useLocale();
  const params = useParams<{ token: string }>();
  const token = decodeURIComponent(params.token ?? "");
  const [formError, setFormError] = useState<string | null>(null);
  const [done, setDone] = useState<Accepted | null>(null);
  const nameId = useId();
  const passwordId = useId();
  const confirmId = useId();

  const preview = useSWR<Preview, ApiError>(
    token ? ["invitation-preview", token] : null,
    () =>
      apiFetch<Preview>("/v1/panel/invitations/preview", {
        body: JSON.stringify({ token }),
        method: "POST",
      }),
    { revalidateOnFocus: false, shouldRetryOnError: false },
  );

  const form = useForm<Values>({
    defaultValues: { confirmPassword: "", displayName: "", password: "" },
    resolver: zodResolver(schema),
  });

  async function submit(values: Values) {
    setFormError(null);
    try {
      const result = await apiFetch<Accepted>("/v1/panel/invitations/accept", {
        body: JSON.stringify({
          displayName: values.displayName.trim(),
          locale: locale === "ru" ? "ru" : "en",
          password: values.password,
          timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
          token,
        }),
        method: "POST",
      });
      if (result.csrfToken) {
        setCsrfToken(result.csrfToken);
      }
      setDone(result);
    } catch (caught) {
      const code = caught instanceof ApiError ? caught.code : null;
      if (code === "invitation_invalid") {
        await preview.mutate();
        return;
      }
      const known: Record<string, string> = {
        email_in_use: "errors.emailInUse",
        rate_limited: "errors.rateLimited",
        weak_password: "errors.weakPassword",
      };
      setFormError(translate(code && known[code] ? known[code] : "errors.generic"));
    }
  }

  return (
    <main className="flex min-h-dvh items-center justify-center bg-background p-4">
      <Card className="w-full max-w-md">
        <CardHeader>
          <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.16em]">
            Omniflow
          </p>
          <CardTitle>{done ? translate("doneTitle") : translate("title")}</CardTitle>
          <CardDescription>
            {done ? translate("doneDescription") : translate("description")}
          </CardDescription>
        </CardHeader>

        <CardContent>
          {done ? (
            <div className="flex flex-col gap-4">
              <Alert variant="info">
                <ShieldCheck />
                <AlertDescription>{translate("secureHint")}</AlertDescription>
              </Alert>
              {done.signedIn ? (
                <div className="flex flex-col gap-2 sm:flex-row">
                  <Button onClick={() => router.replace("/admin/security")}>
                    {translate("secureNow")}
                  </Button>
                  <Button onClick={() => router.replace("/admin")} variant="outline">
                    {translate("skip")}
                  </Button>
                </div>
              ) : (
                <Button asChild>
                  <Link href="/admin/login">{translate("signIn")}</Link>
                </Button>
              )}
            </div>
          ) : preview.isLoading ? (
            <div aria-busy="true" className="flex flex-col gap-3">
              <span className="sr-only">{translate("loading")}</span>
              <Skeleton className="h-5 w-2/3" />
              <Skeleton className="h-9 w-full" />
              <Skeleton className="h-9 w-full" />
            </div>
          ) : preview.error || !preview.data ? (
            <div className="flex flex-col gap-4">
              <Alert variant="danger">
                <AlertTriangle />
                <AlertDescription>
                  {preview.error?.code === "rate_limited"
                    ? translate("errors.rateLimited")
                    : preview.error && preview.error.status !== 404
                      ? translate("errors.unavailable")
                      : translate("invalid")}
                </AlertDescription>
              </Alert>
              <Button asChild variant="outline">
                <Link href="/admin/login">{translate("toSignIn")}</Link>
              </Button>
            </div>
          ) : (
            <form className="flex flex-col gap-4" noValidate onSubmit={form.handleSubmit(submit)}>
              <dl className="grid gap-2 rounded-md border border-border p-3 text-sm">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <dt className="text-muted-foreground">{translate("email")}</dt>
                  <dd className="font-mono text-[13px]">{preview.data.email}</dd>
                </div>
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <dt className="text-muted-foreground">{translate("roles")}</dt>
                  <dd className="flex flex-wrap gap-1">
                    {preview.data.roles.map((role) => (
                      <Badge key={role} variant="neutral">
                        {translateRole(role)}
                      </Badge>
                    ))}
                  </dd>
                </div>
                <p className="text-muted-foreground text-xs">
                  {translate("expires", { date: new Date(preview.data.expiresAt) })}
                </p>
              </dl>

              {formError ? (
                <Alert variant="danger">
                  <AlertTriangle />
                  <AlertDescription>{formError}</AlertDescription>
                </Alert>
              ) : null}

              {/* The address is the account's sign-in name; a hidden field
                  carrying it lets a password manager file the new credential
                  under the right username. */}
              <input autoComplete="username" hidden readOnly value={preview.data.email} />

              <div className="flex flex-col gap-1.5">
                <Label htmlFor={nameId}>{translate("displayName")}</Label>
                <Input
                  aria-invalid={Boolean(form.formState.errors.displayName)}
                  autoComplete="name"
                  id={nameId}
                  {...form.register("displayName")}
                />
                {form.formState.errors.displayName ? (
                  <p className="text-destructive text-xs">{translate("errors.displayName")}</p>
                ) : null}
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={passwordId}>{translate("password")}</Label>
                <Input
                  aria-describedby={`${passwordId}-hint`}
                  aria-invalid={Boolean(form.formState.errors.password)}
                  autoComplete="new-password"
                  id={passwordId}
                  type="password"
                  {...form.register("password")}
                />
                <p className="text-muted-foreground text-xs" id={`${passwordId}-hint`}>
                  {translate("passwordHint")}
                </p>
                <PasswordStrength password={form.watch("password") ?? ""} />
                {form.formState.errors.password ? (
                  <p className="text-destructive text-xs">{translate("errors.passwordLength")}</p>
                ) : null}
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={confirmId}>{translate("confirmPassword")}</Label>
                <Input
                  aria-invalid={Boolean(form.formState.errors.confirmPassword)}
                  autoComplete="new-password"
                  id={confirmId}
                  type="password"
                  {...form.register("confirmPassword")}
                />
                {form.formState.errors.confirmPassword ? (
                  <p className="text-destructive text-xs">{translate("errors.passwordMismatch")}</p>
                ) : null}
              </div>

              <Button disabled={form.formState.isSubmitting} type="submit">
                {form.formState.isSubmitting ? translate("submitting") : translate("submit")}
              </Button>
            </form>
          )}
        </CardContent>
      </Card>
    </main>
  );
}
