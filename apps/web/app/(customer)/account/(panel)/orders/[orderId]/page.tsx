"use client";

import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { toast } from "@omniflow/ui/toast";
import { RefreshCw } from "lucide-react";
import Link from "next/link";
import { useParams, useSearchParams } from "next/navigation";
import { useFormatter, useTranslations } from "next-intl";
import { useEffect, useState } from "react";
import useSWR from "swr";
import {
  FulfillmentNote,
  knownPhase,
  OrderAmounts,
  OrderPhaseBadge,
  OrderProgress,
  PaymentHandoff,
  RefundList,
} from "@/components/account/commerce/order-status";
import { OPERATIONS } from "@/components/account/commerce/plan-card";
import { useProblemMessage } from "@/components/account/commerce/reasons";
import type {
  OrderSummary,
  PaymentChoice,
  PaymentHandle,
} from "@/components/account/commerce/types";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";
import { useMoney } from "@/lib/format";
import { useSubmission } from "@/lib/idempotency";

/** The payment methods this build has copy for; anything else keeps its code. */
const PROVIDERS = ["telegram_stars", "cryptobot", "yookassa", "manual"];

/** Phases that are still moving, and so are worth re-reading on a timer. */
const LIVE_PHASES = ["pending", "awaiting_action", "succeeded", "provisioning"];

/** Order states the API will still cancel; anything later has money against it. */
const CANCELLABLE = ["draft", "pending"];

/**
 * One order, from payment to provisioning.
 *
 * This screen exists because the interesting part of a purchase happens after
 * the customer stops looking at it: a provider redirects them away, a webhook
 * arrives late or never, a worker retries provisioning three times. Every one of
 * those states is read from the server on each render — the phase, the payment,
 * the fulfillment progress — so closing the tab and coming back, or opening the
 * same order in Telegram, shows the same thing. Nothing about where a purchase
 * has got to is remembered in React, which is why reloading mid-provisioning
 * shows provisioning rather than a fresh page pretending nothing has started.
 *
 * The provider's own notification is not trusted to be timely. `POST /refresh`
 * reconciles the payment intent with the provider and returns what the order
 * looks like afterwards, which is a different thing from re-fetching: a
 * re-fetch would faithfully report the same stale row.
 */
export default function OrderPage() {
  const translate = useTranslations("account.commerce");
  const params = useParams<{ orderId: string }>();
  const search = useSearchParams();
  const format = useFormatter();
  const describeProblem = useProblemMessage();
  const payment = useSubmission();

  const key = `/v1/account/orders/${params.orderId}`;
  const { data, error, isLoading, isValidating, mutate } = useSWR<OrderSummary, ApiError>(
    key,
    fetcher,
    {
      // A moving order is polled; a settled one is not. Polling a completed
      // purchase forever would be a request every few seconds for a page whose
      // answer cannot change again.
      refreshInterval: (current) => (current && LIVE_PHASES.includes(current.phase) ? 6_000 : 0),
    },
  );

  const [busy, setBusy] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  // The method the customer picked on this page. It starts from whatever the
  // server knows — the intent's own provider, then the one the checkout
  // recorded — and, only for the redirect straight from the checkout, the URL.
  const [chosenProvider, setChosenProvider] = useState<string>("");

  const choices = data?.paymentChoices ?? [];
  const serverProvider = data?.payment?.provider || data?.preferredProvider || "";
  const urlProvider = search.get("provider") ?? "";
  useEffect(() => {
    if (chosenProvider) {
      return;
    }
    const candidate = serverProvider || urlProvider;
    if (candidate && (choices.length === 0 || choices.some((c) => c.provider === candidate))) {
      setChosenProvider(candidate);
    } else if (choices.length === 1) {
      setChosenProvider(choices[0].provider);
    }
  }, [choices, chosenProvider, serverProvider, urlProvider]);

  if (isLoading) {
    return <ListSkeleton rows={3} />;
  }
  if (error || !data) {
    return (
      <AccountNotice
        action={
          <Button asChild>
            <Link href="/account/orders">{translate("order.backToOrders")}</Link>
          </Button>
        }
        description={translate("order.notFoundDescription")}
        title={translate("order.notFound")}
        variant="danger"
      />
    );
  }

  const phase = knownPhase(data.phase);
  // Once an intent exists the order's own record wins, because it is what the
  // provider will actually settle against. Before that, the method is whatever
  // the customer chose from the server's list of what can settle this order.
  const provider = data.payment?.provider || chosenProvider;
  const owes = data.externalMinor > 0;
  const payable = owes && CANCELLABLE.includes(data.state);

  async function startPayment() {
    setBusy(true);
    const idempotencyKey = payment.begin();
    try {
      const handle = await apiFetch<PaymentHandle>(`${key}/payment`, {
        body: JSON.stringify({ provider }),
        headers: { "Idempotency-Key": idempotencyKey },
        method: "POST",
      });
      payment.settle();
      // The handoff is rendered from the order rather than from this response, so
      // the same panel appears on a reload. Re-reading is how it gets there.
      const fresh = await mutate();
      // "Nothing left to pay" is true only when the order owes nothing. A
      // provider that produced no handoff for an order that still owes money
      // is a different situation, and the re-read order says which.
      if (handle.handoff === "none" && (fresh?.externalMinor ?? data?.externalMinor ?? 0) === 0) {
        toast.success(translate("order.payment.nothingDue"));
      }
    } catch (failure) {
      payment.settle(failure);
      toast.error(describeProblem(failure));
      await mutate();
    } finally {
      setBusy(false);
    }
  }

  async function refresh() {
    setBusy(true);
    try {
      await mutate(await apiFetch<OrderSummary>(`${key}/refresh`, { method: "POST" }), {
        revalidate: false,
      });
    } catch (failure) {
      toast.error(describeProblem(failure));
    } finally {
      setBusy(false);
    }
  }

  async function cancel() {
    setBusy(true);
    try {
      await apiFetch(`${key}/cancel`, { method: "POST" });
      await mutate();
      toast.success(translate("order.cancelled"));
    } catch (failure) {
      toast.error(describeProblem(failure));
      await mutate();
    } finally {
      setBusy(false);
      setCancelling(false);
    }
  }

  return (
    <div aria-busy={isValidating || busy} className="animate-step-in space-y-5">
      <header className="space-y-3 rounded-lg border border-border bg-card p-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <h1 className="truncate font-semibold text-[19px] tracking-[-0.02em]">{data.plan}</h1>
            <p className="mt-1 font-mono text-[11px] text-subtle-foreground">
              {translate(
                `operations.${OPERATIONS.includes(data.operation) ? data.operation : "unknown"}`,
              )}
              {" · "}
              {format.dateTime(new Date(data.createdAt), {
                day: "numeric",
                hour: "2-digit",
                minute: "2-digit",
                month: "short",
              })}
            </p>
          </div>
          <OrderPhaseBadge phase={data.phase} />
        </div>
        <p className="text-[12.5px] leading-relaxed">
          {translate(`order.phase.${phase}.description`)}
        </p>
        <OrderProgress order={data} />
      </header>

      {data.payment ? (
        <PaymentHandoff payment={data.payment} />
      ) : (
        payable && (
          <section className="space-y-3 rounded-lg border border-border bg-card p-4">
            <div>
              <p className="font-semibold text-[13.5px]">{translate("order.payment.title")}</p>
              <p className="mt-1 text-[12.5px] text-muted-foreground leading-relaxed">
                {choices.length > 0
                  ? translate("order.payment.description")
                  : translate("order.payment.noMethod")}
              </p>
            </div>
            {/* The methods come from the server, priced in this order's
                currency, so an arrival from the order list, a reload without the
                checkout's query string, or a second tab can all still pay. */}
            {choices.length > 0 && (
              <OrderMethodPicker
                choices={choices}
                chosen={provider}
                disabled={busy}
                onChange={setChosenProvider}
              />
            )}
            {choices.length > 0 && (
              <Button
                className="w-full"
                disabled={busy || !provider}
                onClick={startPayment}
                size="lg"
              >
                {translate("order.payment.start")}
              </Button>
            )}
          </section>
        )
      )}

      {/* A payment left half-finished can be started again for as long as the
          order is one the API would still cancel — which is exactly the window in
          which money has not moved against it. Starting it is idempotent per
          order and provider, so this resumes the intent that exists rather than
          opening a second one, and the retry carries a new key because the last
          attempt ended in an answer rather than in silence. */}
      {data.payment && payable && provider && (
        <Button className="w-full" disabled={busy} onClick={startPayment} size="lg">
          {translate("order.payment.retry")}
        </Button>
      )}

      {data.fulfillment && <FulfillmentNote fulfillment={data.fulfillment} />}

      <section className="space-y-2">
        <SectionLabel>{translate("order.amounts.title")}</SectionLabel>
        <div className="rounded-lg border border-border bg-card p-4">
          <OrderAmounts order={data} />
        </div>
      </section>

      {data.refunds && data.refunds.length > 0 && (
        <section className="space-y-2">
          <SectionLabel>{translate("order.refunds")}</SectionLabel>
          <RefundList refunds={data.refunds} />
        </section>
      )}

      <div className="space-y-2">
        <Button className="w-full" disabled={busy} onClick={refresh} size="lg" variant="outline">
          <RefreshCw aria-hidden />
          {translate("order.refresh")}
        </Button>
        <p className="px-1 text-center text-[11.5px] text-subtle-foreground leading-relaxed">
          {translate("order.refreshHint")}
        </p>

        {data.subscriptionId && (
          <Button asChild className="w-full" size="lg" variant="outline">
            <Link href={`/account/subscriptions/${data.subscriptionId}`}>
              {translate("order.viewSubscription")}
            </Link>
          </Button>
        )}

        {CANCELLABLE.includes(data.state) && (
          <Button
            className="w-full text-destructive"
            disabled={busy}
            onClick={() => setCancelling(true)}
            size="lg"
            variant="outline"
          >
            {translate("order.cancel")}
          </Button>
        )}
      </div>

      <ConfirmDialog
        cancelLabel={translate("actions.cancel")}
        confirmLabel={translate("order.cancel")}
        description={translate("order.cancelDescription")}
        destructive
        onConfirm={cancel}
        onOpenChange={setCancelling}
        open={cancelling}
        pending={busy}
        title={translate("order.cancel")}
      />
    </div>
  );
}

/**
 * Which method settles this order.
 *
 * The list is the server's, already filtered to what can take this order's
 * currency and priced at what the order still owes, so every option shown is
 * one the payment route will accept.
 */
function OrderMethodPicker({
  choices,
  chosen,
  disabled,
  onChange,
}: {
  choices: PaymentChoice[];
  chosen: string;
  disabled: boolean;
  onChange: (provider: string) => void;
}) {
  const translate = useTranslations("account.commerce");
  const money = useMoney();
  return (
    <fieldset className="space-y-2">
      <legend className="sr-only">{translate("checkout.provider.title")}</legend>
      {choices.map((choice) => (
        <label
          className="flex cursor-pointer items-center gap-3 rounded-lg border border-border bg-card p-3 has-[:checked]:border-primary"
          key={choice.provider}
        >
          <input
            checked={chosen === choice.provider}
            className="size-4 accent-[color:var(--primary)]"
            disabled={disabled}
            name="order-provider"
            onChange={() => onChange(choice.provider)}
            type="radio"
          />
          <span className="min-w-0 flex-1">
            <span className="block font-medium text-[13.5px]">
              {translate(
                `checkout.provider.names.${
                  PROVIDERS.includes(choice.provider) ? choice.provider : "unknown"
                }`,
              )}
            </span>
          </span>
          {choice.amountMinor !== undefined && (
            <span className="shrink-0 font-medium text-[13px]" data-numeric>
              {money(choice.amountMinor, choice.currency)}
            </span>
          )}
        </label>
      ))}
    </fieldset>
  );
}
