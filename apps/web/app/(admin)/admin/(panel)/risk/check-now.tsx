"use client";

import { Button } from "@omniflow/ui/button";
import { Card } from "@omniflow/ui/card";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";

import { ApiError, apiFetch } from "@/lib/api";
import { useSession } from "@/lib/session";

type ScreenResult = { checked: number; newMatches: number; skippedOperators: number };

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * "Check now" against the subscribed blocklists, for one customer or for all
 * of them. The worker screens every customer after each list refresh; this is
 * for an operator who just added a source, or wants an answer about one
 * customer without waiting. Operators and allowlisted customers are skipped,
 * and a match already on record — decided or not — is left as it was.
 */
export function CheckNow({ onChecked }: { onChecked: () => void }) {
  const translate = useTranslations("admin.risk.checkNow");
  const { can } = useSession();
  const inputId = useId();
  const [customerId, setCustomerId] = useState("");
  const [pending, setPending] = useState(false);
  const [result, setResult] = useState<ScreenResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  if (!can("risk.write")) {
    return null;
  }

  async function check(target: string) {
    setPending(true);
    setError(null);
    setResult(null);
    try {
      const answer = await apiFetch<ScreenResult>("/v1/panel/risk/check", {
        method: "POST",
        body: JSON.stringify(target ? { customerId: target } : {}),
      });
      setResult(answer);
      onChecked();
    } catch (caught) {
      setError(caught instanceof ApiError ? caught.message : translate("failed"));
    } finally {
      setPending(false);
    }
  }

  const trimmed = customerId.trim();
  return (
    <Card className="flex flex-col gap-3 p-3">
      <p className="text-muted-foreground text-sm">{translate("description")}</p>
      <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
        <div className="flex flex-1 flex-col gap-1.5">
          <Label htmlFor={inputId}>{translate("customerId")}</Label>
          <Input
            id={inputId}
            onChange={(event) => setCustomerId(event.target.value)}
            placeholder="00000000-0000-0000-0000-000000000000"
            value={customerId}
          />
        </div>
        <Button
          disabled={pending || !UUID.test(trimmed)}
          onClick={() => void check(trimmed)}
          size="sm"
          variant="outline"
        >
          {translate("one")}
        </Button>
        <Button disabled={pending} onClick={() => void check("")} size="sm">
          {translate("all")}
        </Button>
      </div>
      {pending && <p className="text-muted-foreground text-xs">{translate("running")}</p>}
      {result && (
        <p className="text-sm" role="status">
          {translate("result", {
            checked: result.checked,
            matches: result.newMatches,
            operators: result.skippedOperators,
          })}
        </p>
      )}
      {error && <p className="text-danger-foreground text-sm">{error}</p>}
    </Card>
  );
}
