"use client";

import { Button } from "@omniflow/ui/button";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { toast } from "@omniflow/ui/toast";
import { useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { useState } from "react";

import { SectionLabel } from "@/components/account/state";
import { type ApiError, apiFetch } from "@/lib/api";
import { useMoney } from "@/lib/format";

type Redeemed = {
  kind: "subscription" | "wallet_credit" | "traffic" | "loyalty_tier" | "squad_access";
  endsAt?: string;
  creditMinor?: number;
  currency?: string;
  trafficBytes?: number;
};

/** The confirmation for what a code gave. */
function redeemedMessage(
  result: Redeemed,
  translate: (key: string, values?: Record<string, string | number>) => string,
  money: (minor: number, currency: string) => string,
): string {
  switch (result.kind) {
    case "wallet_credit":
      return result.creditMinor !== undefined && result.currency
        ? translate("credited", { amount: money(result.creditMinor, result.currency) })
        : translate("extended");
    case "traffic":
      return translate("traffic", {
        gigabytes: Math.round((result.trafficBytes ?? 0) / 1024 ** 3),
      });
    case "loyalty_tier":
      return translate("tier");
    case "squad_access":
      return translate("squad", {
        until: result.endsAt ? new Date(result.endsAt).toLocaleString() : "",
      });
    default:
      return result.creditMinor && result.currency
        ? translate("extendedAndCredited", { amount: money(result.creditMinor, result.currency) })
        : translate("extended");
  }
}

/**
 * Applies a gift code, an access code, or a promo code that rewards on its
 * own (days, a trial, wallet credit). Discount codes are entered at checkout.
 * Every refusal reads the same, as the API answers it.
 */
export function RedeemCode({ onRedeemed }: { onRedeemed: () => void }) {
  const translate = useTranslations("account.commerce.wallet.redeem");
  const money = useMoney();
  // An activation link from a guest purchase e-mail carries the code as
  // ?code=, so the customer only has to press the button after signing in.
  const params = useSearchParams();
  const [code, setCode] = useState(params?.get("code") ?? "");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setPending(true);
    setError(null);
    try {
      const result = await apiFetch<Redeemed>("/v1/account/codes/redeem", {
        body: JSON.stringify({ code: code.trim() }),
        method: "POST",
      });
      toast.success(redeemedMessage(result, translate, money));
      setCode("");
      onRedeemed();
    } catch (failure) {
      const problem = failure as ApiError;
      setError(problem.code === "rate_limited" ? translate("rateLimited") : translate("refused"));
    } finally {
      setPending(false);
    }
  }

  return (
    <section className="space-y-3">
      <SectionLabel>{translate("title")}</SectionLabel>
      <form className="space-y-2 rounded-lg border border-border bg-card p-3" onSubmit={submit}>
        {/* The section heading already says what goes in the field. */}
        <Label className="sr-only" htmlFor="redeem-code">
          {translate("label")}
        </Label>
        <div className="flex gap-2">
          <Input
            aria-describedby={error ? "redeem-code-error" : undefined}
            aria-invalid={error ? true : undefined}
            autoCapitalize="characters"
            autoComplete="off"
            id="redeem-code"
            onChange={(event) => setCode(event.target.value)}
            placeholder={translate("label")}
            value={code}
          />
          <Button disabled={pending || code.trim().length < 3} type="submit">
            {pending ? translate("applying") : translate("apply")}
          </Button>
        </div>
        {error && (
          <p className="text-[12.5px] text-destructive" id="redeem-code-error" role="alert">
            {error}
          </p>
        )}
      </form>
    </section>
  );
}
