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
import { type ReactNode, useState } from "react";
import useSWR from "swr";

import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { openInTelegramBrowser } from "@/lib/tma";

type TermsDocument = { kind: "terms" | "offer" | "privacy"; slug: string; title: string };
type Terms = {
  required: boolean;
  accepted: boolean;
  version: string;
  termsUrl?: string;
  documents?: TermsDocument[];
};

/**
 * Holds the panel until the customer accepts the terms, when the operator
 * requires it in Settings → Marketing. The dialog cannot be dismissed: the
 * acceptance is a recorded consent, and checkout refuses to confirm without
 * it anyway.
 *
 * It links every document the customer agrees to — the published terms,
 * offer, and privacy policy — and says so when accepting also consents to the
 * processing of personal data. A document opens beside the panel, in a new
 * tab or Telegram's browser, so the dialog is still there to accept.
 */
export function TermsGate() {
  const translate = useTranslations("account.terms");
  const { data, mutate } = useSWR<Terms, ApiError>("/v1/account/terms", fetcher, {
    shouldRetryOnError: false,
  });
  const [pending, setPending] = useState(false);
  const [failed, setFailed] = useState(false);
  const open = Boolean(data?.required && !data.accepted);
  const documents = data?.documents ?? [];

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
        {documents.length > 0 ? (
          <ul className="space-y-1.5">
            {documents.map((document) => (
              <li key={document.slug}>
                <DocumentLink href={`/pages/${document.slug}`}>{document.title}</DocumentLink>
              </li>
            ))}
          </ul>
        ) : (
          data?.termsUrl && <DocumentLink href={data.termsUrl}>{translate("read")}</DocumentLink>
        )}
        {documents.some((document) => document.kind === "privacy") && (
          <p className="text-muted-foreground text-xs">{translate("privacy")}</p>
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

function DocumentLink({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a
      className="text-accent-foreground text-sm underline underline-offset-2"
      href={href}
      onClick={(event) => {
        if (openInTelegramBrowser(href)) {
          event.preventDefault();
        }
      }}
      rel="noreferrer"
      target="_blank"
    >
      {children}
    </a>
  );
}
