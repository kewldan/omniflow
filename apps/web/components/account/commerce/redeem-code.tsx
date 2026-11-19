"use client";

import { Button } from "@omniflow/ui/button";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useState } from "react";

import { SectionLabel } from "@/components/account/state";
import { type ApiError, apiFetch } from "@/lib/api";
import { useMoney } from "@/lib/format";

type Redeemed = {
  kind: "subscription" | "wallet_credit";
  endsAt?: string;
  creditMinor?: number;
  currency?: string;
};

/**
 * Applies a gift code, an access code, or a promo code that rewards on its
 * own (days, a trial, wallet credit). Discount codes are entered at checkout.
 * Every refusal reads the same, as the API answers it.
 */
export function RedeemCode({ onRedeemed }: { onRedeemed: () => void }) {
  const translate = useTranslations("account.commerce.wallet.redeem");
  const money = useMoney();
  const [code, setCode] = useState("");
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
      toast.success(
        result.kind === "wallet_credit" && result.creditMinor !== undefined && result.currency
          ? translate("credited", { amount: money(result.creditMinor, result.currency) })
          : translate("extended"),
      );
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
      <form className="space-y-2 rounded-lg border border-border bg-card p-4" onSubmit={submit}>
        <Label htmlFor="redeem-code">{translate("label")}</Label>
        <div className="flex gap-2">
          <Input
            aria-describedby={error ? "redeem-code-error" : undefined}
            aria-invalid={error ? true : undefined}
            autoCapitalize="characters"
            autoComplete="off"
            id="redeem-code"
            onChange={(event) => setCode(event.target.value)}
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
        <p className="text-[12.5px] text-muted-foreground">{translate("hint")}</p>
      </form>
    </section>
  );
}
