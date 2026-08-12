"use client";

import { cn } from "@omniflow/ui/lib/utils";
import { useTranslations } from "next-intl";

import type { CheckoutQuote } from "@/components/account/commerce/types";
import { useMoney } from "@/lib/format";

/**
 * What this purchase costs, exactly as the server priced it.
 *
 * Every line is a field of the quote. Not one of them is derived here — not the
 * total, not the remainder after the wallet, not even the sum of the add-ons.
 * That restraint is the whole point of the component: pricing is a rule, the
 * rules live in `internal/commerce`, and a panel that recomputed a subtotal
 * would be a second implementation of them. The two would agree for months and
 * then disagree over a rounding case on a zero-decimal currency, and the
 * customer would be shown one number and charged another.
 *
 * The lines are shown only when they carry a value, because a discount of zero
 * and a wallet contribution of zero are noise on a receipt that a customer is
 * reading to check one figure. Deciding whether a number is zero is not a
 * pricing rule; it is deciding whether to print a row.
 */
export function QuoteBreakdown({ className, quote }: { className?: string; quote: CheckoutQuote }) {
  const translate = useTranslations("account.commerce");
  const money = useMoney();
  const amount = (minor: number) => money(minor, quote.currency);

  return (
    <dl className={cn("space-y-2", className)}>
      <Row label={translate("breakdown.subtotal")} value={amount(quote.subtotalMinor)} />
      {quote.addonMinor !== 0 && (
        <Row label={translate("breakdown.addons")} value={amount(quote.addonMinor)} />
      )}
      {quote.discountMinor !== 0 && (
        <Row
          label={translate("breakdown.discount")}
          tone="success"
          value={`−${amount(quote.discountMinor)}`}
        />
      )}
      {quote.walletAppliedMinor !== 0 && (
        <Row
          label={translate("breakdown.wallet")}
          tone="success"
          value={`−${amount(quote.walletAppliedMinor)}`}
        />
      )}

      <div className="flex items-baseline justify-between gap-3 border-border border-t pt-3">
        <dt className="font-medium text-[13px]">{translate("breakdown.external")}</dt>
        <dd className="font-bold text-[18px] tracking-[-0.02em]" data-numeric>
          {amount(quote.externalMinor)}
        </dd>
      </div>

      {/* A wallet-funded order still creates an order; it simply owes a provider
          nothing. Saying so here stops the confirm step from looking broken when
          no payment method needs choosing. */}
      {quote.externalMinor === 0 && (
        <p className="text-[12px] text-muted-foreground leading-relaxed">
          {translate("breakdown.nothingDue")}
        </p>
      )}
    </dl>
  );
}

function Row({ label, tone, value }: { label: string; tone?: "success"; value: string }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="text-[12.5px] text-muted-foreground">{label}</dt>
      <dd
        className={cn("font-medium text-[13px]", tone === "success" && "text-success")}
        data-numeric
      >
        {value}
      </dd>
    </div>
  );
}
