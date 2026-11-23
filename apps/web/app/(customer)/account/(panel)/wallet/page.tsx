"use client";

import { zodResolver } from "@hookform/resolvers/zod";
import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import { toast } from "@omniflow/ui/toast";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useTranslations } from "next-intl";
import { useForm, useWatch } from "react-hook-form";
import useSWR from "swr";
import useSWRInfinite from "swr/infinite";
import * as z from "zod/mini";
import { PaymentHandoff } from "@/components/account/commerce/order-status";
import {
  choiceKey,
  splitChoiceKey,
  useChoiceCopy,
} from "@/components/account/commerce/payment-choice";
import { PaymentMethodOption } from "@/components/account/commerce/payment-method-option";
import { useProblemCode, useProblemMessage } from "@/components/account/commerce/reasons";
import { RedeemCode } from "@/components/account/commerce/redeem-code";
import type {
  OrderSummary,
  TopUpPolicy,
  TopUpResult,
  WalletBalance,
  WalletEntry,
  WalletView,
} from "@/components/account/commerce/types";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { FieldMessage, FormField } from "@/components/forms/field";
import { MoneyInput } from "@/components/forms/money-input";
import { Timestamp } from "@/components/intl/timestamp";
import { type ApiError, apiFetch, fetcher, toQuery } from "@/lib/api";
import { issue, moneyIssue } from "@/lib/form-schema";
import { useMoney } from "@/lib/format";
import { useSubmission } from "@/lib/idempotency";
import { minorToInput, parseMajorToMinor } from "@/lib/money";
import { useRevalidateCommerce } from "@/lib/polling";

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
  const revalidateCommerce = useRevalidateCommerce();
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

      {/* Rendered from the wallet read, never from the form's own state: a
          provider page the customer closed, a reload, and a second tab all
          find the same handoff here, and a top-up the provider refused is
          listed with the way to retry it. */}
      {wallet.pendingTopUps.length > 0 && (
        <section className="space-y-3">
          <SectionLabel>{translate("wallet.pending.title")}</SectionLabel>
          {wallet.pendingTopUps.map((order) => (
            <PendingTopUp key={order.id} order={order} />
          ))}
        </section>
      )}

      {/* A top-up changes the balance shown on the dashboard, the checkout,
          and the order screens too, so every one of them is re-read. */}
      <TopUpForm
        availableMinor={
          wallet.balances.find((balance) => balance.currency === wallet.currency)?.availableMinor ??
          0
        }
        currency={wallet.currency}
        onCredited={() => {
          // The ledger is an infinite list, which a filtered global mutate
          // never matches, so it is re-read through its own handle.
          void mutate();
          void revalidateCommerce();
        }}
        policy={wallet.topUp}
      />

      <RedeemCode
        onRedeemed={() => {
          void mutate();
          void revalidateCommerce();
        }}
      />

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

/**
 * A top-up that has been opened and not yet paid.
 *
 * The handoff is the order's own, so it is the same one on every render of
 * this page. A top-up with no payment yet — the provider refused the intent —
 * links to the order screen, which offers every method that can settle it.
 */
function PendingTopUp({ order }: { order: OrderSummary }) {
  const translate = useTranslations("account.commerce");
  const money = useMoney();
  return (
    <article className="animate-step-in space-y-3 rounded-lg border border-border bg-card p-4">
      <div className="flex items-baseline justify-between gap-3">
        <p className="font-semibold text-[13.5px]">
          {translate("wallet.pending.amount", {
            amount: money(order.externalMinor, order.currency),
          })}
        </p>
        <Link
          className="text-[12.5px] text-subtle-foreground underline underline-offset-2"
          href={`/account/orders/${order.id}`}
        >
          {translate("wallet.pending.open")}
        </Link>
      </div>
      {order.payment ? (
        <PaymentHandoff owes={order.externalMinor > 0} payment={order.payment} />
      ) : (
        <p className="text-[12.5px] text-muted-foreground leading-relaxed">
          {translate("wallet.pending.noPayment")}
        </p>
      )}
    </article>
  );
}

/** One currency's holding, with the part of it that is already claimed. */
function BalanceCard({ balance }: { balance: WalletBalance }) {
  const translate = useTranslations("account.commerce");
  const money = useMoney({ trimZeroFraction: true });

  return (
    <article className="animate-step-in space-y-3 rounded-lg border border-border bg-card p-4">
      <div className="flex items-center justify-between gap-3">
        <span className="text-[14px] text-muted-foreground">{translate("wallet.available")}</span>
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
  availableMinor,
  currency,
  onCredited,
  policy,
}: {
  availableMinor: number;
  currency: string;
  onCredited: () => void;
  policy: TopUpPolicy;
}) {
  const translate = useTranslations("account.commerce");
  const money = useMoney({ trimZeroFraction: true });
  const describeProblem = useProblemMessage();
  const describeCode = useProblemCode();
  const submission = useSubmission();
  // The shop sends a customer here to top up exactly what a saved purchase is
  // missing; the amount arrives prefilled, raised to the smallest top-up the
  // policy accepts, and the screen says the purchase will make itself.
  const searchParams = useSearchParams();
  const requestedMinor = Number(searchParams.get("topup") ?? "");
  const forShop = searchParams.get("for") === "shop";
  const prefilledMinor =
    Number.isInteger(requestedMinor) && requestedMinor > 0
      ? Math.max(requestedMinor, policy.minimumMinor)
      : 0;

  // The input is in the currency's major unit because that is what a person
  // types; the API is in minor units because that is the only representation
  // that survives a round trip. The conversion is the shared one in
  // lib/money.ts, which knows each currency's exponent and refuses more
  // decimals than the currency has rather than rounding them away. The bounds
  // are checked here so the customer hears about them before paying; the store
  // checks them again under the lock that records the credit.
  const schema = z.object({ amount: z.string(), provider: z.string() }).check(
    z.superRefine((values, context) => {
      const problem = moneyIssue(values.amount, currency, {
        formatted: (minor) => money(minor, currency),
        maximumMinor: policy.maximumMinor > 0 ? policy.maximumMinor : undefined,
        minimumMinor: Math.max(policy.minimumMinor, 1),
      });
      if (problem) {
        context.issues.push({
          code: "custom",
          input: values.amount,
          message: problem,
          path: ["amount"],
        });
      }
      if (!values.provider) {
        context.issues.push({
          code: "custom",
          input: values.provider,
          message: issue("chooseOne"),
          path: ["provider"],
        });
      }
    }),
  );
  type Values = z.infer<typeof schema>;
  const { control, formState, handleSubmit, register, reset, setValue } = useForm<Values>({
    defaultValues: {
      amount: prefilledMinor > 0 ? minorToInput(prefilledMinor, currency) : "",
      // One way to pay is not a choice. Starting empty left the submit button
      // disabled with nothing on screen saying which of the one options to pick.
      provider: policy.providers.length === 1 ? choiceKey(policy.providers[0]) : "",
    },
    resolver: zodResolver(schema),
  });
  const parsed = parseMajorToMinor(useWatch({ control, name: "amount" }), currency);
  const minor = parsed.ok ? parsed.minor : 0;
  const copy = useChoiceCopy();
  const chosenKey = useWatch({ control, name: "provider" });
  const chosen = policy.providers.find((choice) => choiceKey(choice) === chosenKey);
  // A method the operator gave its own quick amounts shows those instead of
  // the wallet-wide presets.
  const fixedPresets = chosen?.quickAmountsMinor?.length
    ? chosen.quickAmountsMinor
    : policy.presets;
  const renewalPresets = useRenewalPresets({ availableMinor, currency, policy });
  // Amounts that mean something to this customer — what the next period
  // costs, and what the balance is short of it — replace round figures that
  // meant nothing to anyone. The operator's figures stay for a customer with
  // no subscription to price them against.
  const presets: { amountMinor: number; label: string }[] =
    renewalPresets.length > 0
      ? renewalPresets
      : fixedPresets.map((amountMinor) => ({ amountMinor, label: money(amountMinor, currency) }));

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

  async function submit(values: Values) {
    const amount = parseMajorToMinor(values.amount, currency);
    if (!amount.ok) {
      return;
    }
    const key = submission.begin();
    try {
      const result = await apiFetch<TopUpResult>("/v1/account/wallet/top-up", {
        body: JSON.stringify({
          amountMinor: amount.minor,
          currency,
          ...splitChoiceKey(values.provider),
        }),
        headers: { "Idempotency-Key": key },
        method: "POST",
      });
      submission.settle();
      reset({ amount: "", provider: values.provider });
      // The handoff is rendered from the wallet's pending list, which the
      // re-read below populates, rather than from this response — so it is
      // still there after a reload. The response only decides what to say.
      if (result.paymentProblem) {
        toast.error(describeCode(result.paymentProblem.code));
      } else {
        toast.success(
          translate("wallet.topUp.startedDescription", {
            amount: money(result.amountMinor, result.currency),
          }),
        );
      }
      onCredited();
    } catch (failure) {
      submission.settle(failure);
      toast.error(describeProblem(failure));
    }
  }

  const busy = formState.isSubmitting;

  return (
    <section className="space-y-2">
      <SectionLabel>{translate("wallet.topUp.title")}</SectionLabel>
      {forShop && prefilledMinor > 0 && (
        <p
          className="rounded-lg border border-info/40 bg-info/10 px-4 py-3 text-[12.5px] leading-relaxed"
          role="status"
        >
          {translate("wallet.topUp.forShop", { amount: money(prefilledMinor, currency) })}
        </p>
      )}

      <form
        className="space-y-4 rounded-lg border border-border bg-card p-4"
        noValidate
        onSubmit={handleSubmit(submit)}
      >
        {presets.length > 0 && (
          <div className="flex flex-wrap gap-2">
            {presets.map((preset) => (
              <Button
                key={`${preset.label}-${preset.amountMinor}`}
                onClick={() =>
                  setValue("amount", minorToInput(preset.amountMinor, currency), {
                    shouldDirty: true,
                    shouldValidate: formState.isSubmitted,
                  })
                }
                size="sm"
                type="button"
                variant={minor === preset.amountMinor ? "secondary" : "outline"}
              >
                {preset.label}
              </Button>
            ))}
          </div>
        )}

        <FormField
          error={formState.errors.amount?.message}
          // The per-payment bounds are said by the field when an amount falls
          // outside them; up front, only a window that is tighter than they
          // are is worth a line, because it is the one limit nobody expects.
          hint={
            policy.remainingWindowMinor > 0 &&
            (policy.maximumMinor <= 0 || policy.remainingWindowMinor < policy.maximumMinor)
              ? translate("wallet.topUp.window", {
                  amount: money(policy.remainingWindowMinor, currency),
                })
              : undefined
          }
          label={translate("wallet.topUp.amount")}
        >
          {(props) => <MoneyInput {...props} currency={currency} {...register("amount")} />}
        </FormField>

        <fieldset
          aria-describedby={formState.errors.provider ? "top-up-provider-error" : undefined}
          className="space-y-2"
        >
          <legend className="pb-2 font-semibold text-[13px] text-muted-foreground">
            {translate("checkout.provider.title")}
          </legend>
          {policy.providers.length === 0 ? (
            <p className="text-[12.5px] text-muted-foreground leading-relaxed">
              {translate("checkout.provider.empty")}
            </p>
          ) : (
            <div className="space-y-2">
              {policy.providers.map((choice, index) => (
                <PaymentMethodOption
                  choice={choice}
                  index={index}
                  key={choiceKey(choice)}
                  value={choiceKey(choice)}
                  {...register("provider")}
                >
                  <span className="block font-semibold text-[15px]">{copy(choice).label}</span>
                  {copy(choice).description && (
                    <span className="block text-[12.5px] text-muted-foreground">
                      {copy(choice).description}
                    </span>
                  )}
                  {(choice.minMinor || choice.maxMinor) && (
                    <span className="block text-[12.5px] text-muted-foreground">
                      {translate("checkout.provider.bounds", {
                        maximum: choice.maxMinor ? money(choice.maxMinor, currency) : "—",
                        minimum: choice.minMinor ? money(choice.minMinor, currency) : "—",
                      })}
                    </span>
                  )}
                </PaymentMethodOption>
              ))}
            </div>
          )}
          <FieldMessage id="top-up-provider-error" message={formState.errors.provider?.message} />
        </fieldset>

        {policy.supportTopUp && (
          <p className="text-[12.5px] text-muted-foreground">
            <Link className="underline underline-offset-2" href="/account/support">
              {translate("wallet.topUp.viaSupport")}
            </Link>
          </p>
        )}

        {/* Disabled only when there is truly nothing to pay with, and that is
            said just above; every other problem is reported on its field when
            the customer presses the button, rather than by a button that
            silently refuses. */}
        <Button
          className="w-full"
          disabled={busy || policy.providers.length === 0}
          size="lg"
          type="submit"
        >
          {busy ? translate("wallet.topUp.submitting") : translate("wallet.topUp.submit")}
        </Button>
      </form>
    </section>
  );
}

/** One ledger movement, with the operator's note when a correction carried one. */
function LedgerRow({ entry }: { entry: WalletEntry }) {
  const translate = useTranslations("account.commerce");
  const money = useMoney();
  const type = ENTRY_TYPES.includes(entry.type) ? entry.type : "unknown";
  const credit = entry.amountMinor > 0;

  return (
    <li className="flex animate-rise items-start justify-between gap-3 rounded-md border border-border bg-card p-3">
      <div className="min-w-0">
        <p className="font-medium text-[13px]">{translate(`wallet.entryType.${type}`)}</p>
        <p className="mt-0.5 text-[12.5px] text-subtle-foreground">
          <Timestamp value={entry.occurredAt} />
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

type RenewalPricing = {
  subscriptions: {
    endsAt?: string;
    currency: string;
    periods: { amountMinor: number; own: boolean }[];
  }[];
};

/**
 * The quick amounts worth offering this customer: what their balance is short
 * of the next period, and the period itself.
 *
 * The price is the subscription's own period from the renewal read, for the
 * subscription that ends first in the wallet's currency. The renewal amount is
 * raised to the smallest top-up the policy takes, and either is dropped when
 * it is above what may be added, so a chip never offers an amount the store
 * would refuse.
 * Nothing to price against — no subscription, or the read failed — gives an
 * empty list and the operator's presets.
 */
function useRenewalPresets({
  availableMinor,
  currency,
  policy,
}: {
  availableMinor: number;
  currency: string;
  policy: TopUpPolicy;
}): { amountMinor: number; label: string }[] {
  const translate = useTranslations("account.commerce");
  const money = useMoney({ trimZeroFraction: true });
  const { data } = useSWR<RenewalPricing, ApiError>("/v1/account/renewal", fetcher, {
    shouldRetryOnError: false,
  });

  const next = (data?.subscriptions ?? [])
    .filter((subscription) => subscription.currency === currency)
    .map((subscription) => ({
      endsAt: subscription.endsAt ? Date.parse(subscription.endsAt) : Number.POSITIVE_INFINITY,
      price: subscription.periods.find((period) => period.own)?.amountMinor ?? 0,
    }))
    .filter((subscription) => subscription.price > 0)
    .sort((left, right) => left.endsAt - right.endsAt)[0];
  if (!next) {
    return [];
  }

  const ceiling = Math.min(
    policy.maximumMinor > 0 ? policy.maximumMinor : Number.POSITIVE_INFINITY,
    policy.remainingWindowMinor > 0 ? policy.remainingWindowMinor : Number.POSITIVE_INFINITY,
  );
  const offer = (amountMinor: number) => Math.max(amountMinor, policy.minimumMinor);
  const presets: { amountMinor: number; label: string }[] = [];
  const shortfall = next.price - availableMinor;
  // Offered only as the exact figure: below the smallest top-up, a chip that
  // said "missing 79 ₽" and filled in 100 ₽ would be saying something false,
  // and the renewal chip beside it covers that customer anyway.
  if (shortfall >= policy.minimumMinor && shortfall < next.price && shortfall <= ceiling) {
    presets.push({
      amountMinor: shortfall,
      label: translate("wallet.topUp.quickShortfall", { amount: money(shortfall, currency) }),
    });
  }
  if (offer(next.price) <= ceiling) {
    presets.push({
      amountMinor: offer(next.price),
      label: translate("wallet.topUp.quickRenewal", { amount: money(offer(next.price), currency) }),
    });
  }
  return presets;
}
