"use client";

import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import { Check, Copy } from "lucide-react";
import { useTranslations } from "next-intl";
import { useEffect, useState } from "react";

/**
 * Copies one value — an order ID, a customer ID, a reference — and says so.
 *
 * Both UX audits asked for it: IDs were shown truncated in tables and in full
 * in titles with no way to copy either, so people selected text by hand or
 * copied the UUID out of the address bar. The confirmation is visible (the icon
 * turns into a check) and announced (a polite live region), and a browser that
 * refuses clipboard access — an insecure origin, a denied permission — says
 * that instead of pretending.
 */
export function CopyButton({
  className,
  label,
  value,
}: {
  className?: string;
  /** What is being copied, for the accessible name: "order ID". */
  label: string;
  value: string;
}) {
  const translate = useTranslations("common.copy");
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");

  useEffect(() => {
    if (state === "idle") {
      return;
    }
    const timer = window.setTimeout(() => setState("idle"), 2_000);
    return () => window.clearTimeout(timer);
  }, [state]);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setState("copied");
    } catch {
      setState("failed");
    }
  }

  return (
    <>
      <Button
        aria-label={translate("action", { label })}
        className={cn("size-6 text-muted-foreground", className)}
        onClick={copy}
        size="icon-sm"
        title={translate("action", { label })}
        type="button"
        variant="ghost"
      >
        {state === "copied" ? <Check className="text-success" /> : <Copy />}
      </Button>
      <span aria-live="polite" className="sr-only">
        {state === "copied" ? translate("copied") : state === "failed" ? translate("failed") : ""}
      </span>
    </>
  );
}
