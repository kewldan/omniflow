"use client";

import { useTranslations } from "next-intl";

import { PageHeader } from "@/components/admin/resource-table";
import { useSession } from "@/lib/session";

import { CurrencyCard } from "./currency";
import { SelfEmployedCard } from "./self-employed";
import { WithdrawalsCard } from "./withdrawals";

/**
 * Money leaving or being recorded outside the order flow: referral
 * withdrawals waiting for a decision, and the policy behind them.
 */
export function PayoutsWorkspace() {
  const translate = useTranslations("admin.payouts");
  const { can } = useSession();
  return (
    <div className="flex flex-col gap-5">
      <PageHeader description={translate("description")} title={translate("title")} />
      <WithdrawalsCard editable={can("finance.write")} />
      <SelfEmployedCard editable={can("finance.write")} />
      <CurrencyCard editable={can("finance.write")} />
    </div>
  );
}
