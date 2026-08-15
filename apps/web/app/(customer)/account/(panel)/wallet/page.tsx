"use client";

import { Button } from "@omniflow/ui/button";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { cn } from "@omniflow/ui/lib/utils";
import { toast } from "@omniflow/ui/toast";
import { useFormatter, useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWRInfinite from "swr/infinite";
import { PaymentHandoff } from "@/components/account/commerce/order-status";
import { useProblemMessage } from "@/components/account/commerce/reasons";
import type {
  TopUpPolicy,
  TopUpResult,
  WalletBalance,
  WalletEntry,
  WalletView,
} from "@/components/account/commerce/types";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { type ApiError, apiFetch, fetcher, toQuery } from "@/lib/api";
import { currencyExponent, useMoney } from "@/lib/format";
import { useSubmission } from "@/lib/idempotency";

/** The ledger movements this build has copy for, from `ledger_transactions.type`. */
const ENTRY_TYPES = [
  "credit",
  "debit",
  "payment",
  "refund",
  "referral_reward",
  "correction",
  "expiration",
];

/** The payment methods this build has copy for. */
const PROVIDERS = ["telegram_stars", "cryptobot", "yookassa", "manual"];

/**
 * The wallet: what is held, what is spoken for, and how to add more.
 *
 * The three figures per currency are the whole reason this screen is not a
 * single number. The total is what the ledger holds, the reserved part is what
 * unpaid orders have already claimed, and the available part is what a new
 * purchase may actually spend. All three arrive computed — subtracting one from
 * another here would be this panel deciding what a customer can spend, which is
 * a rule the order path owns and enforces under a lock this screen does not take.
 */
export default function WalletPage() {
  const translate = useTranslations("account.commerce");
  const { data, error, isLoading, isValidating, mutate, setSize, size } = useSWRInfinite<
    WalletView,
    ApiError
  >((index, previous) => {
    if (index > 0 && !previous?.nextCursor) {
      return null;
    }
    return `/v1/account/wallet${toQuery({
      cursor: index === 0 ? undefined : previous?.nextCursor,
      cursorId: index === 0 ? undefined : previous?.nextCursorId,
      limit: 20,
    })}`;
  }, fetcher);

  if (isLoading) {
    return <ListSkeleton rows={3} />;
  }
  if (error || !data || data.length === 0) {
    return (
      <AccountNotice
        description={translate("store.errorDescription")}
        title={translate("store.error")}
        variant="danger"
      />
    );
  }

  // Balances and the top-up policy belong to the wallet, not to a page of the
  // ledger, so they are read from the first response and not re-read from the
  // ones fetched for older history.
  const wallet = data[0];
  const entries = data.flatMap((page) => page.entries);
  const more = Boolean(data[data.length - 1]?.nextCursor);

  return (
    <div className="space-y-5">
      <section className="space-y-3">
        {wallet.balances.map((balance) => (
          <BalanceCard balance={balance} key={balance.currency} />
        ))}
      </section>

      <TopUpForm onCredited={() => mutate()} policy={wallet.topUp} currency={wallet.currency} />

      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <SectionLabel>{translate("wallet.ledger.title")}</SectionLabel>
        </div>
        {entries.length === 0 ? (
          <AccountNotice
            description={translate("wallet.ledger.emptyDescription")}
            title={translate("wallet.ledger.empty")}
          />
        ) : (
          <ul aria-busy={isValidating} className="space-y-2">
            {entries.map((entry) => (
              <LedgerRow entry={entry} key={entry.id} />
            ))}
          </ul>
        )}
        {more && (
          <Button
            className="w-full"
            disabled={isValidating}
            onClick={() => setSize(size + 1)}
            size="lg"
            variant="outline"
          >
            {translate("actions.loadMore")}
          </Button>
        )}
      </section>
    </div>
  );
}

/** One currency's holding, with the part of it that is already claimed. */
function BalanceCard({ balance }: { balance: WalletBalance }) {
  const translate = useTranslations("account.commerce");
  const money = useMoney();

  return (
    <article className="animate-step-in space-y-3 rounded-lg border border-border bg-card p-4">
      <div className="flex items-baseline justify-between gap-3">
        <span className="font-mono text-[10.5px] text-subtle-foreground uppercase tracking-[0.14em]">
          {translate("wallet.available")}
        </span>
        <span className="font-bold text-[26px] leading-none tracking-[-0.04em]" data-numeric>
          {money(balance.availableMinor, balance.currency)}
        </span>
      </div>
      {/* Total and reserved are the breakdown of the headline. With nothing
          reserved they are the same figure twice, and printing a number beside
          itself under two different words invites the reader to look for the
          difference. */}
      {balance.reservedMinor > 0 && (
        <dl className="space-y-1.5 border-border border-t pt-3">
          <div className="flex items-baseline justify-between gap-3">
            <dt className="text-[12px] text-muted-foreground">{translate("wallet.total")}</dt>
            <dd className="font-medium text-[12.5px]" data-numeric>
              {money(balance.totalMinor, balance.currency)}
            </dd>
          </div>
          <div className="flex items-baseline justify-between gap-3">
            <dt className="text-[12px] text-muted-foreground">{translate("wallet.reserved")}</dt>
            <dd className="font-medium text-[12.5px] text-warning" data-numeric>
              {money(balance.reservedMinor, balance.currency)}
            </dd>
          </div>
        </dl>
      )}
      {balance.reservedMinor > 0 && (
        <p className="text-[11.5px] text-subtle-foreground leading-relaxed">
          {translate("wallet.reservedHint")}
        </p>
      )}
    </article>
  );
}

/**
 * Adding credit.
 *
 * Every bound on the form comes from the operator's configuration as the API
 * reports it: the presets are already filtered against what this customer has
 * credited in the rolling window, so an offered amount is one that would be
 * accepted. The minimum and maximum are shown rather than enforced — the store
 * validates them under the same lock that records the credit, and a refusal
 * arrives with a reason this screen has copy for.
 */
function TopUpForm({
  currency,
  onCredited,
  policy,
}: {
  currency: string;
  onCredited: () => void;
  policy: TopUpPolicy;
}) {
  const translate = useTranslations("account.commerce");
  const locale = useLocale();
  const money = useMoney();
  const describeProblem = useProblemMessage();
  const submission = useSubmission();

  const [amount, setAmount] = useState("");
  // One way to pay is not a choice. Starting empty left the submit button
  // disabled with nothing on screen saying which of the one options to pick.
  const [provider, setProvider] = useState(() =>
    policy.providers.length === 1 ? policy.providers[0].provider : "",
  );
  const [busy, setBusy] = useState(false);
  const [started, setStarted] = useState<TopUpResult | null>(null);

  if (!policy.enabled) {
    return (
      <section className="space-y-2">
        <SectionLabel>{translate("wallet.topUp.title")}</SectionLabel>
        <p className="rounded-lg border border-border bg-card p-4 text-[12.5px] text-muted-foreground leading-relaxed">
          {translate("wallet.topUp.disabled")}
        </p>
      </section>
    );
  }

  // The input is in the currency's major unit because that is what a person
  // types; the API is in minor units because that is the only representation
  // that survives a round trip. The exponent comes from Intl rather than from a
  // hard-coded 100, which would misprice every zero-decimal currency an operator
  // might configure.
  const exponent = currencyExponent(currency, locale);
  const minor = Math.round(Number(amount.replace(",", ".")) * 10 ** exponent);
  const usable = Number.isFinite(minor) && minor > 0;

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    const key = submission.begin();
    try {
      const result = await apiFetch<TopUpResult>("/v1/account/wallet/top-up", {
        body: JSON.stringify({ amountMinor: minor, currency, provider }),
        headers: { "Idempotency-Key": key },
        method: "POST",
      });
      submission.settle();
      setStarted(result);
      onCredited();
    } catch (failure) {
      submission.settle(failure);
      toast.error(describeProblem(failure));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="space-y-2">
      <SectionLabel>{translate("wallet.topUp.title")}</SectionLabel>

      {started ? (
        <div className="space-y-3">
          <div className="rounded-lg border border-border bg-card p-4">
            <p className="font-semibold text-[13.5px]">{translate("wallet.topUp.started")}</p>
            <p className="mt-1 text-[12.5px] text-muted-foreground leading-relaxed">
              {translate("wallet.topUp.startedDescription", {
                amount: money(started.amountMinor, started.currency),
              })}
            </p>
          </div>
          <PaymentHandoff payment={started.payment} />
          <Button
            className="w-full"
            onClick={() => {
              setStarted(null);
              setAmount("");
              onCredited();
            }}
            size="lg"
            variant="outline"
          >
            {translate("wallet.topUp.another")}
          </Button>
        </div>
      ) : (
        <form className="space-y-4 rounded-lg border border-border bg-card p-4" onSubmit={submit}>
          {policy.presets.length > 0 && (
            <div className="flex flex-wrap gap-2">
              {policy.presets.map((preset) => (
                <Button
                  key={preset}
                  onClick={() => setAmount(String(preset / 10 ** exponent))}
                  size="sm"
                  type="button"
                  variant={minor === preset ? "secondary" : "outline"}
                >
                  {money(preset, currency)}
                </Button>
              ))}
            </div>
          )}

          <div className="space-y-2">
            <Label htmlFor="top-up-amount">{translate("wallet.topUp.amount", { currency })}</Label>
            <Input
              autoComplete="off"
              id="top-up-amount"
              inputMode="decimal"
              onChange={(event) => setAmount(event.target.value)}
              placeholder="0"
              required
              value={amount}
            />
            <p className="text-[11.5px] text-subtle-foreground leading-relaxed">
              {translate("wallet.topUp.limits", {
                maximum: money(policy.maximumMinor, currency),
                minimum: money(policy.minimumMinor, currency),
              })}
              {policy.remainingWindowMinor > 0 &&
                ` ${translate("wallet.topUp.remaining", {
                  amount: money(policy.remainingWindowMinor, currency),
                })}`}
            </p>
          </div>

          <fieldset className="space-y-2">
            <legend className="pb-2 font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
              {translate("checkout.provider.title")}
            </legend>
            {policy.providers.length === 0 ? (
              <p className="text-[12.5px] text-muted-foreground leading-relaxed">
                {translate("checkout.provider.empty")}
              </p>
            ) : (
              <div className="space-y-2">
                {policy.providers.map((choice) => (
                  <label
                    className={cn(
                      "flex cursor-pointer items-center gap-3 rounded-md border border-border p-3",
                      "has-[:checked]:border-primary",
                    )}
                    key={choice.provider}
                  >
                    <input
                      checked={provider === choice.provider}
                      className="size-4 accent-[color:var(--primary)]"
                      name="top-up-provider"
                      onChange={() => setProvider(choice.provider)}
                      required
                      type="radio"
                      value={choice.provider}
                    />
                    <span className="font-medium text-[13.5px]">
                      {translate(
                        `checkout.provider.names.${
                          PROVIDERS.includes(choice.provider) ? choice.provider : "unknown"
                        }`,
                      )}
                    </span>
                  </label>
                ))}
              </div>
            )}
          </fieldset>

          <Button
            className="w-full"
            disabled={busy || !usable || policy.providers.length === 0}
            size="lg"
            type="submit"
          >
            {translate("wallet.topUp.submit")}
          </Button>
        </form>
      )}
    </section>
  );
}

/** One ledger movement, with the operator's note when a correction carried one. */
function LedgerRow({ entry }: { entry: WalletEntry }) {
  const translate = useTranslations("account.commerce");
  const format = useFormatter();
  const money = useMoney();
  const type = ENTRY_TYPES.includes(entry.type) ? entry.type : "unknown";
  const credit = entry.amountMinor > 0;

  return (
    <li className="flex animate-rise items-start justify-between gap-3 rounded-md border border-border bg-card p-3">
      <div className="min-w-0">
        <p className="font-medium text-[13px]">{translate(`wallet.entryType.${type}`)}</p>
        <p className="mt-0.5 font-mono text-[11px] text-subtle-foreground">
          {format.dateTime(new Date(entry.occurredAt), {
            day: "numeric",
            hour: "2-digit",
            minute: "2-digit",
            month: "short",
          })}
        </p>
        {entry.reason && (
          <p className="mt-1 text-[11.5px] text-muted-foreground leading-relaxed">{entry.reason}</p>
        )}
      </div>
      <span
        className={cn(
          "shrink-0 font-medium text-[13px]",
          credit ? "text-success" : "text-foreground",
        )}
        data-numeric
      >
        {credit ? "+" : ""}
        {money(entry.amountMinor, entry.currency)}
      </span>
    </li>
  );
}
