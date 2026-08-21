"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { ConfirmDialog } from "@omniflow/ui/confirm-dialog";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Switch } from "@omniflow/ui/switch";
import { toast } from "@omniflow/ui/toast";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { useEffect, useRef, useState } from "react";
import { OPERATIONS, usePeriodLabel } from "@/components/account/commerce/plan-card";
import { QuoteBreakdown } from "@/components/account/commerce/quote-breakdown";
import {
  useProblemCode,
  useProblemMessage,
  usePromoRejection,
} from "@/components/account/commerce/reasons";
import {
  type CheckoutView,
  type OrderSummary,
  targetLive,
} from "@/components/account/commerce/types";
import { useOpenCheckout } from "@/components/account/commerce/use-checkout";
import { AccountNotice, ListSkeleton, SectionLabel } from "@/components/account/state";
import { apiFetch } from "@/lib/api";
import { attachAttribution } from "@/lib/attach-attribution";
import { useBytes, useDuration, useMoney } from "@/lib/format";
import { useSubmission } from "@/lib/idempotency";

/** The payment methods this build has copy for; anything else keeps its code. */
const PROVIDERS = ["telegram_stars", "cryptobot", "yookassa", "manual"];

/**
 * The confirmation screen.
 *
 * Everything on it is one object from one request. That is deliberate on the
 * server's side and load-bearing on this one: the chosen method fixes the
 * currency, the currency fixes the price, the price and the wallet balance fix
 * what is still owed, and a promo code can move all three. Each control below
 * therefore sends its change and re-renders from the checkout that comes back,
 * rather than adjusting a local copy — there is no arrangement of these controls
 * this page can show that the server would not accept, because the server is
 * what drew it.
 */
export default function CheckoutPage() {
  const translate = useTranslations("account.commerce");
  const { checkout, error, loading, missing, mutate } = useOpenCheckout();
  const router = useRouter();
  const describeProblem = useProblemMessage();
  const [discarding, setDiscarding] = useState(false);

  if (loading) {
    return <ListSkeleton rows={3} />;
  }
  // Having no checkout open is the ordinary state of a customer who is not
  // buying anything. It is answered with a 404 and rendered as a signpost, never
  // as a failure.
  if (missing) {
    return (
      <AccountNotice
        action={
          <Button asChild>
            <Link href="/account/store">{translate("checkout.browse")}</Link>
          </Button>
        }
        description={translate("checkout.noneDescription")}
        title={translate("checkout.none")}
      />
    );
  }
  if (error || !checkout) {
    // A checkout that cannot be read is still a checkout the customer holds,
    // and it blocks every other purchase until it is gone. The way out has to
    // be on this screen, because the store shows only a "resume" banner that
    // leads straight back here.
    return (
      <AccountNotice
        action={
          <Button
            disabled={discarding}
            onClick={async () => {
              setDiscarding(true);
              try {
                await apiFetch("/v1/account/checkout", { method: "DELETE" });
                router.push("/account/store");
              } catch (failure) {
                toast.error(describeProblem(failure));
                setDiscarding(false);
              }
            }}
            variant="outline"
          >
            {translate("checkout.discard")}
          </Button>
        }
        description={translate("checkout.unreadableDescription")}
        title={translate("store.error")}
        variant="danger"
      />
    );
  }
  return <Checkout checkout={checkout} reload={mutate} />;
}

function Checkout({
  checkout,
  reload,
}: {
  checkout: CheckoutView;
  reload: (value?: CheckoutView, options?: { revalidate?: boolean }) => Promise<unknown>;
}) {
  const translate = useTranslations("account.commerce");
  const router = useRouter();
  const money = useMoney();
  const period = usePeriodLabel();
  const describeProblem = useProblemMessage();
  const describeCode = useProblemCode();
  const confirmation = useSubmission();

  const [busy, setBusy] = useState(false);
  const [discarding, setDiscarding] = useState(false);

  /**
   * Applies one edit and adopts the checkout the API returns.
   *
   * The response is the whole re-priced screen, so it replaces the cache without
   * a revalidation: refetching would show the customer the same numbers a second
   * time for no reason. A refusal is different — the screen and the server have
   * diverged, and the only safe thing to render is whatever the server actually
   * holds.
   */
  async function apply(request: () => Promise<CheckoutView>) {
    setBusy(true);
    try {
      await reload(await request(), { revalidate: false });
    } catch (failure) {
      toast.error(describeProblem(failure));
      await reload();
    } finally {
      setBusy(false);
    }
  }

  const patch = (body: Record<string, unknown>) =>
    apply(() =>
      apiFetch<CheckoutView>("/v1/account/checkout", {
        body: JSON.stringify(body),
        method: "PATCH",
      }),
    );

  async function confirm() {
    setBusy(true);
    // One key for this submission, minted now and kept for as long as it is
    // being retried, so a double tap or a resent request resolves to the order
    // that already exists rather than opening a second one.
    const key = confirmation.begin();
    try {
      const order = await apiFetch<OrderSummary>("/v1/account/checkout/confirm", {
        headers: { "Idempotency-Key": key },
        method: "POST",
      });
      confirmation.settle();
      // Where this purchase came from, if an advertisement brought them here
      // and they agreed to measurement. Deliberately not awaited and never
      // allowed to fail the purchase: a customer who has just paid must reach
      // their order whatever the operator's analytics is doing.
      void attachAttribution(order.id);
      // The chosen method travels to the order screen because a freshly created
      // order has no payment intent yet, and so no provider of its own to read.
      router.push(`/account/orders/${order.id}?provider=${encodeURIComponent(checkout.provider)}`);
    } catch (failure) {
      confirmation.settle(failure);
      toast.error(describeProblem(failure));
      await reload();
    } finally {
      setBusy(false);
    }
  }

  async function discard() {
    setBusy(true);
    try {
      await apiFetch("/v1/account/checkout", { method: "DELETE" });
      router.push("/account/store");
    } catch (failure) {
      toast.error(describeProblem(failure));
    } finally {
      setBusy(false);
      setDiscarding(false);
    }
  }

  const operation = OPERATIONS.includes(checkout.operation) ? checkout.operation : "unknown";
  // Until the server has a price there is nothing to pay for yet; the button
  // reads as a confirmation and stays disabled, rather than promising a free
  // order because a withheld quote says zero.
  const unresolved = checkout.squadSelection.required || !checkout.quoteAvailable;
  const needsPayment = unresolved || checkout.quote.externalMinor > 0;
  // A checkout that still owes money and names no method would become an order
  // nobody can pay: the payment route needs a provider, and a created order
  // carries none until one is started. The guard is here rather than in a
  // refusal after the fact, which would leave the customer holding that order.
  // An unfinished server choice blocks the same way: the server has said what
  // it still needs, and Confirm waits for it.
  const blocked = unresolved || (needsPayment && !checkout.provider) || checkout.targetRequired;

  return (
    <div className="animate-step-in space-y-5" aria-busy={busy}>
      <header className="space-y-2 rounded-lg border border-border bg-card p-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <h1 className="font-semibold text-[19px] tracking-[-0.02em]">{checkout.plan.name}</h1>
            <p className="mt-1 font-mono text-[11px] text-subtle-foreground">
              {period(checkout.plan.billingPeriod, checkout.plan.durationSeconds)}
            </p>
          </div>
          <Badge variant="outline">{translate(`operations.${operation}`)}</Badge>
        </div>
        <ExpiryNotice expiresAt={checkout.expiresAt} />
      </header>

      {checkout.multiSubscription && checkout.subscriptions.length > 0 && (
        <TargetPicker checkout={checkout} disabled={busy} onChange={patch} />
      )}

      {checkout.squads.configurable && (
        <SquadPicker checkout={checkout} disabled={busy} onChange={patch} />
      )}

      {checkout.addons.length > 0 && (
        <AddonPicker checkout={checkout} disabled={busy} onToggle={apply} />
      )}

      <PromoField checkout={checkout} disabled={busy} onApply={apply} />

      {/* An empty wallet has nothing to spend, so the control that would spend it
          is not a decision — it is a switch whose two positions do the same
          thing, sitting above a balance of zero. */}
      {checkout.quote.walletBalanceMinor > 0 && (
        <section className="space-y-2">
          <SectionLabel>{translate("checkout.wallet.title")}</SectionLabel>
          <div className="flex items-start justify-between gap-4 rounded-lg border border-border bg-card p-4">
            <div className="min-w-0">
              <Label className="font-medium text-[13.5px]" htmlFor="apply-wallet">
                {translate("checkout.wallet.label")}
              </Label>
              <p className="mt-1 text-[12px] text-muted-foreground leading-relaxed">
                {translate("checkout.wallet.available", {
                  amount: money(checkout.quote.walletBalanceMinor, checkout.quote.currency),
                })}
              </p>
            </div>
            <Switch
              checked={checkout.applyWallet}
              disabled={busy}
              id="apply-wallet"
              onCheckedChange={(next) => patch({ applyWallet: next })}
            />
          </div>
        </section>
      )}

      <ProviderPicker checkout={checkout} disabled={busy} onChange={patch} />

      <section className="space-y-2">
        <SectionLabel>{translate("checkout.total")}</SectionLabel>
        <div className="rounded-lg border border-border bg-card p-4">
          {checkout.quoteAvailable ? (
            <QuoteBreakdown quote={checkout.quote} />
          ) : (
            <p className="text-[12.5px] text-muted-foreground leading-relaxed" role="status">
              {translate("checkout.squads.incomplete")}
            </p>
          )}
        </div>
      </section>

      {checkout.squadSelection.required && (
        <p
          className="rounded-lg border border-warning/40 bg-warning/10 px-4 py-3 text-[12.5px] leading-relaxed"
          role="status"
        >
          {describeCode(checkout.squadSelection.reason)}
        </p>
      )}

      {checkout.targetRequired && (
        <p
          className="rounded-lg border border-warning/40 bg-warning/10 px-4 py-3 text-[12.5px] leading-relaxed"
          role="status"
        >
          {translate("checkout.target.required")}
        </p>
      )}

      <div className="space-y-2">
        <Button className="w-full" disabled={busy || blocked} onClick={confirm} size="lg">
          {translate(needsPayment ? "checkout.confirm" : "checkout.confirmFree")}
        </Button>
        {blocked && !checkout.targetRequired && !unresolved && (
          <p className="px-1 text-center text-[11.5px] text-warning" role="status">
            {translate("checkout.provider.required")}
          </p>
        )}
        {checkout.termsUrl && (
          <p className="px-1 text-center text-[11.5px] text-subtle-foreground">
            <a
              className="underline underline-offset-2"
              href={checkout.termsUrl}
              rel="noopener noreferrer"
              target="_blank"
            >
              {translate("plan.terms")}
            </a>
          </p>
        )}
        <Button
          className="w-full text-destructive"
          disabled={busy}
          onClick={() => setDiscarding(true)}
          size="lg"
          variant="outline"
        >
          {translate("checkout.discard")}
        </Button>
      </div>

      <ConfirmDialog
        cancelLabel={translate("actions.cancel")}
        confirmLabel={translate("checkout.discard")}
        description={translate("checkout.discardDescription")}
        destructive
        onConfirm={discard}
        onOpenChange={setDiscarding}
        open={discarding}
        pending={busy}
        title={translate("checkout.discard")}
      />
    </div>
  );
}

/**
 * How long the quote stands.
 *
 * A checkout is deleted on read once it lapses, so an expired one is not a
 * warning the customer can act on later — it is the screen becoming a catalogue
 * again on the next load. The countdown re-renders on its own because a number
 * that says "12 minutes" for an hour is worse than no number at all.
 */
function ExpiryNotice({ expiresAt }: { expiresAt?: string }) {
  const translate = useTranslations("account.commerce");
  const remaining = useDuration();
  const [, setTick] = useState(0);

  useEffect(() => {
    const timer = setInterval(() => setTick((value) => value + 1), 30_000);
    return () => clearInterval(timer);
  }, []);

  if (!expiresAt) {
    return null;
  }
  const { expired, minutes } = remaining(expiresAt);
  return (
    <p className="font-mono text-[11px] text-subtle-foreground" role="status">
      {expired ? translate("checkout.expired") : translate("checkout.expiresIn", { minutes })}
    </p>
  );
}

/** Which subscription this purchase changes. */
function TargetPicker({
  checkout,
  disabled,
  onChange,
}: {
  checkout: CheckoutView;
  disabled: boolean;
  onChange: (body: Record<string, unknown>) => Promise<void>;
}) {
  const translate = useTranslations("account.commerce");
  // A purchase never targets a subscription that still has time left; those
  // are served by extension, upgrade, and downgrade, and offering them here
  // would hand the customer a refusal after they chose.
  const targets =
    checkout.operation === "purchase"
      ? checkout.subscriptions.filter((subscription) => !targetLive(subscription))
      : checkout.subscriptions;
  return (
    <fieldset className="space-y-2">
      <legend className="px-1 pb-2 font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
        {translate("checkout.target.title")}
      </legend>
      <div className="space-y-2">
        {checkout.operation === "purchase" && (
          <label className="flex cursor-pointer items-center gap-3 rounded-lg border border-border bg-card p-3 has-[:checked]:border-primary">
            <input
              checked={checkout.newSubscription}
              className="size-4 accent-[color:var(--primary)]"
              disabled={disabled}
              name="checkout-target"
              onChange={() => onChange({ newSubscription: true })}
              type="radio"
            />
            <span className="font-medium text-[13.5px]">{translate("checkout.target.new")}</span>
          </label>
        )}
        {targets.map((subscription) => (
          <label
            className="flex cursor-pointer items-center gap-3 rounded-lg border border-border bg-card p-3 has-[:checked]:border-primary"
            key={subscription.id}
          >
            <input
              checked={checkout.subscriptionId === subscription.id}
              className="size-4 accent-[color:var(--primary)]"
              disabled={disabled}
              name="checkout-target"
              onChange={() => onChange({ newSubscription: false, subscriptionId: subscription.id })}
              type="radio"
            />
            <span className="min-w-0">
              <span className="block font-medium text-[13.5px]">{subscription.label}</span>
              <span className="block font-mono text-[11px] text-subtle-foreground">
                {subscription.plan}
              </span>
            </span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}

/**
 * Which servers the subscription is placed on.
 *
 * The bounds come from the plan and are shown as guidance, not enforced here: the
 * selection is validated by `commerce.ResolveSquads` when the order is created,
 * and a second implementation of the same counting rule in React is exactly what
 * would eventually let through a set the order then refuses.
 */
function SquadPicker({
  checkout,
  disabled,
  onChange,
}: {
  checkout: CheckoutView;
  disabled: boolean;
  onChange: (body: Record<string, unknown>) => Promise<void>;
}) {
  const translate = useTranslations("account.commerce");
  const selected = new Set(checkout.selectedSquadIds);

  return (
    <fieldset className="space-y-2">
      <legend className="px-1 pb-2 font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
        {translate("checkout.squads.title")}
      </legend>
      <p className="px-1 pb-1 text-[12px] text-muted-foreground leading-relaxed">
        {checkout.squads.maximum === null
          ? translate("checkout.squads.minimum", { minimum: checkout.squads.minimum })
          : translate("checkout.squads.range", {
              maximum: checkout.squads.maximum,
              minimum: checkout.squads.minimum,
            })}
      </p>
      <div className="space-y-2">
        {checkout.squads.offered.map((squad) => (
          <label
            className="flex cursor-pointer items-center gap-3 rounded-lg border border-border bg-card p-3 has-[:checked]:border-primary"
            key={squad.squadId}
          >
            <input
              checked={selected.has(squad.squadId)}
              className="size-4 accent-[color:var(--primary)]"
              disabled={disabled}
              onChange={() => {
                const next = new Set(selected);
                if (!next.delete(squad.squadId)) {
                  next.add(squad.squadId);
                }
                return onChange({ squadIds: [...next] });
              }}
              type="checkbox"
            />
            <span className="font-medium text-[13.5px]">{squad.label}</span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}

/** The optional extras, each priced by the server the moment it is toggled. */
function AddonPicker({
  checkout,
  disabled,
  onToggle,
}: {
  checkout: CheckoutView;
  disabled: boolean;
  onToggle: (request: () => Promise<CheckoutView>) => Promise<void>;
}) {
  const translate = useTranslations("account.commerce");
  const money = useMoney();
  const formatBytes = useBytes();
  const chosen = new Set(checkout.selectedAddons.map((addon) => addon.addonVersionId));

  return (
    <section className="space-y-2">
      <SectionLabel>{translate("checkout.addons.title")}</SectionLabel>
      <ul className="space-y-2">
        {checkout.addons.map((addon) => {
          const active = chosen.has(addon.addonVersionId);
          return (
            <li key={addon.addonVersionId}>
              <button
                aria-pressed={active}
                className="flex w-full items-start justify-between gap-3 rounded-lg border border-border bg-card p-4 text-left transition-colors aria-pressed:border-primary focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2"
                disabled={disabled}
                onClick={() =>
                  onToggle(() =>
                    apiFetch<CheckoutView>(`/v1/account/checkout/addons/${addon.addonVersionId}`, {
                      method: "POST",
                    }),
                  )
                }
                type="button"
              >
                <span className="min-w-0">
                  <span className="block font-medium text-[13.5px]">{addon.name}</span>
                  {addon.trafficBytes !== null && (
                    <span className="mt-1 block font-mono text-[11px] text-subtle-foreground">
                      {translate("plan.addonTraffic", { amount: formatBytes(addon.trafficBytes) })}
                    </span>
                  )}
                </span>
                <span className="shrink-0 font-medium text-[13px]" data-numeric>
                  {money(addon.price.amountMinor, addon.price.currency)}
                </span>
              </button>
            </li>
          );
        })}
      </ul>
      <p className="px-1 text-[11.5px] text-subtle-foreground">
        {translate("checkout.addons.hint")}
      </p>
    </section>
  );
}

/**
 * The promo code and, when it is refused, the reason.
 *
 * A refusal comes back as a 200 carrying a stable reason, not as an error, and
 * it is rendered that way: the checkout survives it and the customer can try
 * another code without losing what they had already configured.
 */
function PromoField({
  checkout,
  disabled,
  onApply,
}: {
  checkout: CheckoutView;
  disabled: boolean;
  onApply: (request: () => Promise<CheckoutView>) => Promise<void>;
}) {
  const translate = useTranslations("account.commerce");
  const explain = usePromoRejection();
  const [code, setCode] = useState("");

  return (
    <section className="space-y-2">
      <SectionLabel>{translate("checkout.promo.title")}</SectionLabel>
      {checkout.promoCode ? (
        <div className="flex items-center justify-between gap-3 rounded-lg border border-success/40 bg-card p-4">
          <span className="min-w-0">
            <span className="block font-medium text-[13.5px]">{checkout.promoCode}</span>
            <span className="block text-[12px] text-muted-foreground">
              {translate("checkout.promo.applied")}
            </span>
          </span>
          <Button
            disabled={disabled}
            onClick={() =>
              onApply(() =>
                apiFetch<CheckoutView>("/v1/account/checkout/promo", { method: "DELETE" }),
              )
            }
            size="sm"
            variant="ghost"
          >
            {translate("checkout.promo.remove")}
          </Button>
        </div>
      ) : (
        <form
          className="flex gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            return onApply(() =>
              apiFetch<CheckoutView>("/v1/account/checkout/promo", {
                body: JSON.stringify({ code }),
                method: "POST",
              }),
            );
          }}
        >
          <Label className="sr-only" htmlFor="promo-code">
            {translate("checkout.promo.label")}
          </Label>
          <Input
            autoComplete="off"
            id="promo-code"
            onChange={(event) => setCode(event.target.value)}
            placeholder={translate("checkout.promo.placeholder")}
            value={code}
          />
          <Button disabled={disabled || code.trim() === ""} type="submit" variant="outline">
            {translate("checkout.promo.apply")}
          </Button>
        </form>
      )}
      {checkout.promoRejection && (
        <p
          className="rounded-lg border border-warning/40 bg-warning/10 px-4 py-3 text-[12.5px] leading-relaxed"
          role="status"
        >
          {explain(checkout.promoRejection)}
        </p>
      )}
    </section>
  );
}

/**
 * How the remainder is paid.
 *
 * The list is exactly what the API offers, which is already narrowed to the
 * methods the operator configured that can settle a currency this plan is priced
 * in. Choosing one is what fixes the order currency, so the price beside each
 * method is the price that method would actually charge — the same plan can cost
 * a different number through a different provider, and hiding that until the
 * provider's own page would be a nasty surprise.
 */
function ProviderPicker({
  checkout,
  disabled,
  onChange,
}: {
  checkout: CheckoutView;
  disabled: boolean;
  onChange: (body: Record<string, unknown>) => Promise<void>;
}) {
  const translate = useTranslations("account.commerce");
  const money = useMoney();
  const single = checkout.providers.length === 1 ? checkout.providers[0].provider : null;
  const chosen = checkout.provider;
  const claimed = useRef(false);

  // One way to pay is not a choice, and presenting it as one produces a screen
  // whose confirm button is disabled under a note telling the customer to pick
  // the only option on it. The ref keeps this to a single request: the response
  // sets `checkout.provider`, which is what stops it from firing again.
  useEffect(() => {
    if (!single || chosen || disabled || claimed.current) {
      return;
    }
    claimed.current = true;
    void onChange({ provider: single }).catch(() => {
      claimed.current = false;
    });
  }, [chosen, disabled, onChange, single]);

  return (
    <fieldset className="space-y-2">
      <legend className="px-1 pb-2 font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
        {translate("checkout.provider.title")}
      </legend>
      {checkout.providers.length === 0 ? (
        <p className="rounded-lg border border-border bg-card p-4 text-[12.5px] text-muted-foreground leading-relaxed">
          {translate("checkout.provider.empty")}
        </p>
      ) : (
        <div className="space-y-2">
          {checkout.providers.map((choice) => (
            <label
              className="flex cursor-pointer items-center gap-3 rounded-lg border border-border bg-card p-3 has-[:checked]:border-primary"
              key={choice.provider}
            >
              <input
                checked={checkout.provider === choice.provider}
                className="size-4 accent-[color:var(--primary)]"
                disabled={disabled}
                name="checkout-provider"
                onChange={() => onChange({ provider: choice.provider })}
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
                {choice.recurring && (
                  <span className="block font-mono text-[11px] text-subtle-foreground">
                    {translate("checkout.provider.recurring")}
                  </span>
                )}
              </span>
              {choice.amountMinor !== undefined && (
                <span className="shrink-0 font-medium text-[13px]" data-numeric>
                  {money(choice.amountMinor, choice.currency)}
                </span>
              )}
            </label>
          ))}
        </div>
      )}
    </fieldset>
  );
}
