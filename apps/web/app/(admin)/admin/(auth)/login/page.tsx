"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { Alert, AlertDescription } from "@omniflow/ui/alert";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { AlertTriangle } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { useEffect, useId, useState } from "react";
import { useForm } from "react-hook-form";
import useSWR from "swr";
import { z } from "zod";

import { ApiError, apiFetch, fetcher, setCsrfToken } from "@/lib/api";

const credentialsSchema = z.object({
  email: z.email(),
  password: z.string().min(1),
});

const challengeSchema = z.object({
  // Accepts both a 6-digit TOTP code and a formatted recovery code, so the
  // operator does not have to tell the form which one they are using.
  code: z.string().trim().min(6).max(24),
});

type LoginResult = { challengeRequired?: boolean; csrfToken: string };

/**
 * Operator sign-in.
 *
 * The password step and the second factor are two states of one screen rather
 * than two routes, because the challenge is only meaningful for the session the
 * password step just created.
 */
export default function LoginPage() {
  const translate = useTranslations("admin.login");
  const router = useRouter();
  const searchParams = useSearchParams();
  const [stage, setStage] = useState<"credentials" | "challenge">("credentials");
  const [formError, setFormError] = useState<string | null>(null);
  const emailId = useId();
  const passwordId = useId();
  const codeId = useId();

  // A fresh installation has no operator at all, so sign-in would be a dead end.
  const { data: bootstrap } = useSWR<{ setupRequired: boolean }>("/v1/panel/bootstrap", fetcher, {
    revalidateOnFocus: false,
  });

  useEffect(() => {
    if (bootstrap?.setupRequired) {
      router.replace("/admin/setup");
    }
  }, [bootstrap?.setupRequired, router]);

  const next = searchParams.get("next") ?? "/admin";

  const credentialsForm = useForm<z.infer<typeof credentialsSchema>>({
    defaultValues: { email: "", password: "" },
    resolver: zodResolver(credentialsSchema),
  });
  const challengeForm = useForm<z.infer<typeof challengeSchema>>({
    defaultValues: { code: "" },
    resolver: zodResolver(challengeSchema),
  });

  /** Maps a problem code to copy, falling back to a neutral message. */
  function describe(error: unknown): string {
    if (error instanceof ApiError) {
      switch (error.code) {
        case "invalid_credentials":
          return translate("errors.invalidCredentials");
        case "account_locked":
          return translate("errors.locked");
        case "rate_limited":
          return translate("errors.rateLimited");
        case "invalid_code":
          return translate("errors.invalidCode");
        default:
          return translate("errors.generic");
      }
    }
    return translate("errors.network");
  }

  async function submitCredentials(values: z.infer<typeof credentialsSchema>) {
    setFormError(null);
    try {
      const result = await apiFetch<LoginResult>("/v1/panel/auth/login", {
        body: JSON.stringify(values),
        method: "POST",
      });
      setCsrfToken(result.csrfToken);
      if (result.challengeRequired) {
        setStage("challenge");
        return;
      }
      router.replace(next);
    } catch (error) {
      setFormError(describe(error));
    }
  }

  async function submitChallenge(values: z.infer<typeof challengeSchema>) {
    setFormError(null);
    try {
      const result = await apiFetch<LoginResult>("/v1/panel/auth/challenge", {
        body: JSON.stringify(values),
        method: "POST",
      });
      setCsrfToken(result.csrfToken);
      router.replace(next);
    } catch (error) {
      setFormError(describe(error));
      challengeForm.reset({ code: "" });
    }
  }

  return (
    <main className="flex min-h-dvh items-center justify-center bg-background p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.16em]">
            Omniflow
          </p>
          {/* The card's title is this page's only heading, so it is the level-one
              heading rather than the level-two a CardTitle renders by default.
              A document whose outline starts at h2 gives a screen-reader user no
              top-level landmark to jump to, and on a page that is nothing but a
              sign-in form that is the whole outline. */}
          <h1
            className="font-semibold text-[15px] leading-none tracking-tight"
            data-slot="card-title"
          >
            {stage === "credentials" ? translate("title") : translate("challengeTitle")}
          </h1>
          <CardDescription>
            {stage === "credentials" ? translate("description") : translate("challengeDescription")}
          </CardDescription>
        </CardHeader>

        <CardContent>
          {formError && (
            <Alert className="mb-4" variant="danger">
              <AlertTriangle />
              <AlertDescription>{formError}</AlertDescription>
            </Alert>
          )}

          {stage === "credentials" ? (
            <form
              className="flex flex-col gap-4"
              noValidate
              onSubmit={credentialsForm.handleSubmit(submitCredentials)}
            >
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={emailId}>{translate("email")}</Label>
                <Input
                  aria-invalid={Boolean(credentialsForm.formState.errors.email)}
                  autoComplete="username"
                  id={emailId}
                  type="email"
                  {...credentialsForm.register("email")}
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={passwordId}>{translate("password")}</Label>
                <Input
                  aria-invalid={Boolean(credentialsForm.formState.errors.password)}
                  autoComplete="current-password"
                  id={passwordId}
                  type="password"
                  {...credentialsForm.register("password")}
                />
              </div>
              <Button disabled={credentialsForm.formState.isSubmitting} type="submit">
                {translate("submit")}
              </Button>
            </form>
          ) : (
            <form
              className="flex flex-col gap-4"
              noValidate
              onSubmit={challengeForm.handleSubmit(submitChallenge)}
            >
              <div className="flex flex-col gap-1.5">
                <Label htmlFor={codeId}>{translate("code")}</Label>
                <Input
                  aria-invalid={Boolean(challengeForm.formState.errors.code)}
                  autoComplete="one-time-code"
                  // Not `numeric`: a recovery code contains letters, and forcing
                  // a numeric keypad would make one of the two paths untypable.
                  inputMode="text"
                  id={codeId}
                  {...challengeForm.register("code")}
                />
                <p className="text-muted-foreground text-xs">{translate("codeHint")}</p>
              </div>
              <Button disabled={challengeForm.formState.isSubmitting} type="submit">
                {translate("verify")}
              </Button>
            </form>
          )}
        </CardContent>
      </Card>
    </main>
  );
}
