"use client";

import { Button } from "@omniflow/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@omniflow/ui/dialog";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { type ApiError, apiFetch, fetcher } from "@/lib/api";

type Terms = { required: boolean; accepted: boolean; version: string; termsUrl?: string };

/**
 * Holds the panel until the customer accepts the terms, when the operator
 * requires it in Settings → Marketing. The dialog cannot be dismissed: the
 * acceptance is a recorded consent, and checkout refuses to confirm without
 * it anyway.
 */
export function TermsGate() {
  const translate = useTranslations("account.terms");
  const { data, mutate } = useSWR<Terms, ApiError>("/v1/account/terms", fetcher, {
    shouldRetryOnError: false,
  });
  const [pending, setPending] = useState(false);
  const [failed, setFailed] = useState(false);
  const open = Boolean(data?.required && !data.accepted);

  async function accept() {
    if (!data) {
      return;
    }
    setPending(true);
    setFailed(false);
    try {
      const result = await apiFetch<Terms>("/v1/account/terms/accept", {
        body: JSON.stringify({ version: data.version }),
        method: "POST",
      });
      await mutate(result, { revalidate: false });
    } catch {
      setFailed(true);
      void mutate();
    } finally {
      setPending(false);
    }
  }

  return (
    <Dialog open={open}>
      <DialogContent
        hideClose
        onEscapeKeyDown={(event) => event.preventDefault()}
        onInteractOutside={(event) => event.preventDefault()}
      >
        <DialogHeader>
          <DialogTitle>{translate("title")}</DialogTitle>
          <DialogDescription>{translate("description")}</DialogDescription>
        </DialogHeader>
        {data?.termsUrl && (
          <a
            className="text-primary text-sm underline underline-offset-2"
            href={data.termsUrl}
            rel="noreferrer"
            target="_blank"
          >
            {translate("read")}
          </a>
        )}
        {failed && (
          <p className="text-destructive text-sm" role="alert">
            {translate("failed")}
          </p>
        )}
        <DialogFooter>
          <Button disabled={pending} onClick={() => void accept()}>
            {translate("accept")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
