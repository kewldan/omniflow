"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { Alert, AlertDescription } from "@omniflow/ui/alert";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { AlertTriangle, Info } from "lucide-react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { useEffect, useId, useState } from "react";
import { useForm } from "react-hook-form";
import useSWR from "swr";
import { z } from "zod";

import { ApiError, apiFetch, fetcher } from "@/lib/api";

// Mirrors the server policy in internal/adminauth: length is the control that
// matters, and no composition rules are imposed.
const schema = z
  .object({
    confirmPassword: z.string(),
    displayName: z.string().trim().min(1).max(80),
    email: z.email(),
    password: z.string().min(12).max(256),
    setupToken: z.string().trim().min(1),
  })
  .refine((values) => values.password === values.confirmPassword, {
    message: "passwordMismatch",
    path: ["confirmPassword"],
  });

/**
 * First-owner bootstrap.
 *
 * The setup token is printed to the API log on first start and redeemed once.
 * The screen redirects away as soon as an operator account exists, so a stale
 * tab cannot sit on a form that can no longer be submitted.
 */
export default function SetupPage() {
  const translate = useTranslations("admin.setup");
  const router = useRouter();
  const [formError, setFormError] = useState<string | null>(null);
  const tokenId = useId();
  const emailId = useId();
  const nameId = useId();
  const passwordId = useId();
  const confirmId = useId();

  const { data: bootstrap, isLoading } = useSWR<{ setupRequired: boolean }>(
    "/v1/panel/bootstrap",
    fetcher,
    { revalidateOnFocus: false },
  );

  useEffect(() => {
    if (bootstrap && !bootstrap.setupRequired) {
      router.replace("/admin/login");
    }
  }, [bootstrap, router]);

  const form = useForm<z.infer<typeof schema>>({
    defaultValues: {
      confirmPassword: "",
      displayName: "",
      email: "",
      password: "",
      setupToken: "",
    },
    resolver: zodResolver(schema),
  });

  async function submit(values: z.infer<typeof schema>) {
    setFormError(null);
    try {
      await apiFetch("/v1/panel/bootstrap", {
        body: JSON.stringify({
          displayName: values.displayName,
          email: values.email,
          password: values.password,
          setupToken: values.setupToken,
        }),
        method: "POST",
      });
      router.replace("/admin/login");
    } catch (error) {
      if (error instanceof ApiError && error.code === "bootstrap_closed") {
        setFormError(translate("errors.closed"));
        return;
      }
      if (error instanceof ApiError && error.code === "weak_password") {
        setFormError(translate("errors.weakPassword"));
        return;
      }
      setFormError(translate("errors.generic"));
    }
  }

  if (isLoading) {
    return (
      <main aria-busy="true" className="flex min-h-dvh items-center justify-center p-4">
        <span className="sr-only">{translate("loading")}</span>
      </main>
    );
  }

  return (
    <main className="flex min-h-dvh items-center justify-center bg-background p-4">
      <Card className="w-full max-w-md">
        <CardHeader>
          <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.16em]">
            Omniflow
          </p>
          <CardTitle>{translate("title")}</CardTitle>
          <CardDescription>{translate("description")}</CardDescription>
        </CardHeader>

        <CardContent>
          <Alert className="mb-4" variant="info">
            <Info />
            <AlertDescription>{translate("tokenHint")}</AlertDescription>
          </Alert>

          {formError && (
            <Alert className="mb-4" variant="danger">
              <AlertTriangle />
              <AlertDescription>{formError}</AlertDescription>
            </Alert>
          )}

          <form className="flex flex-col gap-4" noValidate onSubmit={form.handleSubmit(submit)}>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={tokenId}>{translate("setupToken")}</Label>
              <Input
                autoComplete="off"
                className="font-mono text-[13px]"
                id={tokenId}
                {...form.register("setupToken")}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={nameId}>{translate("displayName")}</Label>
              <Input autoComplete="name" id={nameId} {...form.register("displayName")} />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={emailId}>{translate("email")}</Label>
              <Input
                autoComplete="username"
                id={emailId}
                type="email"
                {...form.register("email")}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor={passwordId}>{translate("password")}</Label>
              <Input
                aria-describedby={`${passwordId}-hint`}
                autoComplete="new-password"
                id={passwordId}
                type="password"
                {...form.register("password")}
              />
              <p className="text-muted-foreground text-xs" id={`${passwordId}-hint`}>
                {translate("passwordHint")}
              </p>
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
              {form.formState.errors.confirmPassword && (
                <p className="text-destructive text-xs">{translate("errors.passwordMismatch")}</p>
              )}
            </div>

            <Button disabled={form.formState.isSubmitting} type="submit">
              {translate("submit")}
            </Button>
          </form>
        </CardContent>
      </Card>
    </main>
  );
}
