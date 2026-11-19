"use client";

import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { useTranslations } from "next-intl";
import { useState } from "react";

import { apiFetch } from "@/lib/api";

type State = "idle" | "working" | "done" | "invalid" | "failed";

export function UnsubscribeForm({ token }: { token: string }) {
  const translate = useTranslations("public.unsubscribe");
  const [state, setState] = useState<State>(token ? "idle" : "invalid");

  async function confirm() {
    setState("working");
    try {
      await apiFetch("/v1/account/email/unsubscribe", {
        body: JSON.stringify({ token }),
        method: "POST",
      });
      setState("done");
    } catch (error) {
      const status = (error as { status?: number }).status;
      setState(status === 404 ? "invalid" : "failed");
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription aria-live="polite">
          {state === "done"
            ? translate("done")
            : state === "invalid"
              ? translate("invalid")
              : state === "failed"
                ? translate("failed")
                : translate("description")}
        </CardDescription>
      </CardHeader>
      {(state === "idle" || state === "working" || state === "failed") && (
        <CardContent>
          <Button disabled={state === "working"} onClick={confirm} type="button">
            {translate("confirm")}
          </Button>
        </CardContent>
      )}
    </Card>
  );
}
