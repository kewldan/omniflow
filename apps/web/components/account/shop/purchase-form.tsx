"use client";

import { Button } from "@omniflow/ui/button";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { Switch } from "@omniflow/ui/switch";
import { toast } from "@omniflow/ui/toast";
import { Minus, Plus } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { useCallback, useId, useRef, useState } from "react";
import useSWR from "swr";
import { useGoodsMeasure } from "@/components/account/shop/labels";
import { useShopProblem } from "@/components/account/shop/problem";
import {
  PriceChangeNotice,
  QuoteCountdown,
  QuoteSummary,
} from "@/components/account/shop/quote-panel";
import { type ConfirmedRecipient, RecipientStep } from "@/components/account/shop/recipient-step";
import {
  MAX_QUANTITY,
  payableMinor,
  type ShopOrder,
  type ShopProductDetail,
  type ShopQuote,
} from "@/components/account/shop/types";
import { AccountNotice, ListSkeleton } from "@/components/account/state";
import { type ApiError, apiFetch, fetcher, toQuery } from "@/lib/api";
import { useMoney } from "@/lib/format";

/** Promo refusals this build has copy for; anything else reads as "invalid". */
const PROMO_REJECTIONS = new Set(["below_cost", "exhausted", "ineligible", "invalid", "unknown"]);

/** How long an automatic re-quote waits before it may fire again. */
const REQUOTE_INTERVAL_MS = 5_000;

/**
 * The purchase screen for one product.
 *
 * Three promises shape everything below.
 *
 * The number on the screen is the number charged. The quote is fetched with the
 * product, carries its own expiry, is echoed back to the server on purchase,
 * and is refreshed rather than submitted once it lapses. If the server says the
 * price moved, both numbers are shown side by side and buying at the new one is
 * a separate, deliberate press.
 *
 * The recipient is agreed to before payment is ever reached, in its normalised
 * form, in a step of its own.
 *
 * Every attempt at one purchase carries the same idempotency key, so a
 * double-tapped button, a retried fetch, or a lost response resolves to one
 * order. A key is minted afresh only when the purchase itself changes — a new
 * price, a different quantity, another recipient — because that is a different
 * purchase and must not be swallowed as a duplicate of the last one.
 */
export function PurchaseForm({ productId }: { productId: string }) {
  const translate = useTranslations("account.shop");
  const describe = useShopProblem();
  const money = useMoney();
  const measure = useGoodsMeasure();
  const router = useRouter();
  const quantityId = useId();
  const promoId = useId();
  const walletId = useId();

  const [quantity, setQuantity] = useState(1);
  const [promoInput, setPromoInput] = useState("");
  const [promoCode, setPromoCode] = useState("");
  const [useWallet, setUseWallet] = useState(false);
  const [forSelf, setForSelf] = useState(false);
  const [recipient, setRecipient] = useState<ConfirmedRecipient | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<{ text: string; tone: "bad" | "warn" } | null>(null);
  const [priceChange, setPriceChange] = useState<{ after: ShopQuote; before: ShopQuote } | null>(
    null,
  );

  const key = `/v1/account/shop/products/${productId}${toQuery({ promoCode, quantity })}`;
  const { data, error, isLoading, mutate } = useSWR<ShopProductDetail, ApiError>(key, fetcher, {
    // The displayed price only changes when the customer changes something or
    // the hold lapses. A background revalidation swapping the amount under a
    // reader's eyes would defeat the point of holding it at all.
    keepPreviousData: true,
    revalidateOnFocus: false,
  });

  const lastRequote = useRef(0);
  const idempotency = useRef<{ key: string; signature: string } | null>(null);

  /**
   * Fetches a fresh quote when the hold lapses.
   *
   * It is throttled because a server clock a little ahead of the browser's can
   * hand back a quote that is already expired here, and an untimed re-quote
   * would then loop once a second forever.
   */
  const requote = useCallback(() => {
    const now = Date.now();
    if (now - lastRequote.current < REQUOTE_INTERVAL_MS) {
      return;
    }
    lastRequote.current = now;
    void mutate();
  }, [mutate]);

  if (isLoading) {
    return <ListSkeleton rows={3} />;
  }
  if (error || !data) {
    return <DetailProblem error={error} onRetry={() => mutate()} />;
  }

  const rejection = data.promo?.rejection;
  const discountMinor = data.promo && !rejection ? data.promo.discountMinor : 0;
  const total = payableMinor(data.quote.priceMinor, discountMinor);
  const label = measure(data);

  function changeQuantity(next: number) {
    setQuantity(Math.min(MAX_QUANTITY, Math.max(1, Math.round(next))));
    setPriceChange(null);
    setNotice(null);
  }

  /**
   * The key for one purchase attempt.
   *
   * The signature is every input the order is made of. Retrying the same
   * submission reuses the key; changing anything about the purchase mints a new
   * one, so the server can tell "again" from "another".
   */
  function idempotencyKey(signature: string): string {
    if (idempotency.current?.signature === signature) {
      return idempotency.current.key;
    }
    const minted = crypto.randomUUID();
    idempotency.current = { key: minted, signature };
    return minted;
  }

  async function buy(detail: ShopProductDetail) {
    if (!recipient) {
      return;
    }
    const quote = detail.quote;
    const signature = [
      productId,
      quantity,
      recipient.handle,
      forSelf,
      promoCode,
      useWallet,
      quote.priceMinor,
      quote.currency,
      quote.expiresAt,
    ].join("|");

    setBusy(true);
    setNotice(null);
    try {
      const order = await apiFetch<ShopOrder>("/v1/account/shop/purchase", {
        body: JSON.stringify({
          forSelf,
          productId,
          promoCode: detail.promo && !detail.promo.rejection ? detail.promo.code : undefined,
          quantity,
          // The normalised handle the review step returned, byte for byte. The
          // server refuses anything else, which is what keeps that step
          // load-bearing rather than decorative.
          recipient: recipient.handle,
          quote,
          useWallet,
        }),
        headers: { "Idempotency-Key": idempotencyKey(signature) },
        method: "POST",
      });
      idempotency.current = null;
      setPriceChange(null);
      toast.success(translate("purchase.success"));
      router.push(`/account/shop/orders/${order.id}`);
    } catch (purchaseError) {
      await handleFailure(purchaseError, quote);
    } finally {
      setBusy(false);
    }
  }

  async function handleFailure(purchaseError: unknown, shown: ShopQuote) {
    const code = (purchaseError as ApiError | undefined)?.code ?? "";

    // Nothing was promised past the hold, so a lapsed quote is refreshed
    // without ceremony — but the customer is told, because the amount on the
    // button may now be a different one.
    if (code === "quote_expired") {
      idempotency.current = null;
      lastRequote.current = Date.now();
      await mutate();
      setNotice({ text: translate("problems.quote_expired"), tone: "warn" });
      return;
    }

    // A moved price is never charged silently. The customer agreed to a
    // number; they are entitled to see the new one and decide again.
    if (code === "price_changed") {
      idempotency.current = null;
      lastRequote.current = Date.now();
      const refreshed = await mutate();
      if (refreshed) {
        setPriceChange({ after: refreshed.quote, before: shown });
      } else {
        setNotice({ text: translate("problems.price_changed"), tone: "warn" });
      }
      return;
    }

    // The handle was not the reviewed one, or the gateway refused it. Either
    // way the agreement is void and the step has to be walked again.
    if (code === "recipient_not_reviewed" || code === "recipient_invalid") {
      setRecipient(null);
    }
    setNotice({ text: describe(purchaseError), tone: "bad" });
  }

  return (
    <div className="animate-step-in space-y-4">
      <header className="space-y-2">
        <div className="flex flex-wrap items-baseline gap-2">
          <h1 className="font-semibold text-[19px] tracking-[-0.02em]">{data.name}</h1>
          {label && <span className="font-mono text-[12px] text-subtle-foreground">{label}</span>}
        </div>
        {data.description && (
          <p className="text-[13px] text-muted-foreground leading-relaxed">{data.description}</p>
        )}
      </header>

      {!data.available ? (
        <AccountNotice
          action={
            <Button asChild variant="outline">
              <Link href="/account/shop">{translate("actions.back")}</Link>
            </Button>
          }
          description={translate("product.unavailableDescription")}
          title={translate("product.unavailable")}
          variant="offline"
        />
      ) : (
        <>
          <section className="space-y-3 rounded-lg border border-border bg-card p-4">
            <Label htmlFor={quantityId}>{translate("product.quantity")}</Label>
            <div className="flex items-center gap-2">
              <Button
                aria-label={translate("product.decrease")}
                disabled={quantity <= 1 || busy}
                onClick={() => changeQuantity(quantity - 1)}
                size="icon"
                variant="outline"
              >
                <Minus aria-hidden />
              </Button>
              <Input
                className="w-16 text-center font-mono"
                disabled={busy}
                id={quantityId}
                inputMode="numeric"
                max={MAX_QUANTITY}
                min={1}
                onChange={(event) => changeQuantity(Number(event.target.value))}
                type="number"
                value={quantity}
              />
              <Button
                aria-label={translate("product.increase")}
                disabled={quantity >= MAX_QUANTITY || busy}
                onClick={() => changeQuantity(quantity + 1)}
                size="icon"
                variant="outline"
              >
                <Plus aria-hidden />
              </Button>
            </div>
            <p className="text-[12px] text-muted-foreground leading-relaxed">
              {translate("product.quantityHint", { max: MAX_QUANTITY })}
            </p>
          </section>

          <section className="space-y-2 rounded-lg border border-border bg-card p-4">
            <Label htmlFor={promoId}>{translate("product.promo.label")}</Label>
            <div className="flex gap-2">
              <Input
                autoCapitalize="characters"
                autoComplete="off"
                disabled={busy}
                id={promoId}
                onChange={(event) => setPromoInput(event.target.value)}
                placeholder={translate("product.promo.placeholder")}
                spellCheck={false}
                value={promoInput}
              />
              {promoCode ? (
                <Button
                  disabled={busy}
                  onClick={() => {
                    setPromoCode("");
                    setPromoInput("");
                  }}
                  variant="outline"
                >
                  {translate("product.promo.remove")}
                </Button>
              ) : (
                <Button
                  disabled={busy || promoInput.trim() === ""}
                  onClick={() => setPromoCode(promoInput.trim())}
                >
                  {translate("product.promo.apply")}
                </Button>
              )}
            </div>
            {/* A refused code keeps its own reason: a typo, a code that ran
                out, and a code that does not fit this product are three
                different next moves. */}
            {rejection && (
              <p className="text-[12.5px] text-destructive leading-relaxed" role="status">
                {translate(
                  `product.promo.rejected.${PROMO_REJECTIONS.has(rejection) ? rejection : "invalid"}`,
                )}
              </p>
            )}
            {discountMinor > 0 && data.promo && (
              <p className="text-[12.5px] text-success leading-relaxed" role="status">
                {translate("product.promo.applied", {
                  amount: money(discountMinor, data.quote.currency),
                  code: data.promo.code,
                })}
              </p>
            )}
          </section>

          {/* The wallet is spent only on an explicit yes. A balance applied by
              omission is money moved without being asked for. */}
          <section className="space-y-2 rounded-lg border border-border bg-card p-4">
            <div className="flex items-center gap-2.5">
              <Switch
                checked={useWallet}
                disabled={busy}
                id={walletId}
                onCheckedChange={setUseWallet}
              />
              <Label htmlFor={walletId}>{translate("product.wallet.label")}</Label>
            </div>
            <p className="text-[12px] text-muted-foreground leading-relaxed">
              {translate("product.wallet.hint")}
            </p>
          </section>

          <RecipientStep
            confirmed={recipient}
            disabled={busy}
            forSelf={forSelf}
            onConfirmedChange={setRecipient}
            onForSelfChange={setForSelf}
            productId={productId}
          />

          <section className="space-y-3 rounded-lg border border-border bg-card p-4">
            <QuoteSummary discountMinor={discountMinor} quote={data.quote} />
            <QuoteCountdown expiresAt={data.quote.expiresAt} onExpire={requote} />
          </section>

          {priceChange && (
            <PriceChangeNotice
              after={priceChange.after}
              before={priceChange.before}
              busy={busy}
              onAccept={() => {
                setPriceChange(null);
                void buy(data);
              }}
              onDismiss={() => setPriceChange(null)}
            />
          )}

          {notice && (
            <p
              className={
                notice.tone === "bad"
                  ? "rounded-lg border border-destructive/40 bg-destructive/10 px-4 py-3 text-[12.5px] leading-relaxed"
                  : "rounded-lg border border-warning/40 bg-warning/10 px-4 py-3 text-[12.5px] leading-relaxed"
              }
              role="alert"
            >
              {notice.text}
            </p>
          )}

          <div className="space-y-2">
            <Button
              className="w-full"
              disabled={busy || !recipient || priceChange !== null}
              onClick={() => buy(data)}
              size="lg"
            >
              {busy
                ? translate("purchase.pending")
                : translate("purchase.confirm", { amount: money(total, data.quote.currency) })}
            </Button>
            {!recipient && (
              <p className="px-1 text-center text-[12px] text-muted-foreground">
                {translate("recipient.pending")}
              </p>
            )}
          </div>
        </>
      )}
    </div>
  );
}

/**
 * Why the product could not be shown.
 *
 * The refusals are separated because the remedies are: a shop that does not
 * sell here is permanent, maintenance is over shortly, and a price the provider
 * would not give is worth retrying in a minute.
 */
function DetailProblem({ error, onRetry }: { error?: ApiError; onRetry: () => void }) {
  const translate = useTranslations("account.shop");
  const code = error?.code ?? "";

  if (code === "not_found" || error?.status === 404) {
    return (
      <AccountNotice
        action={
          <Button asChild variant="outline">
            <Link href="/account/shop">{translate("actions.back")}</Link>
          </Button>
        }
        description={translate("states.notFoundDescription")}
        title={translate("states.notFound")}
      />
    );
  }
  if (code === "shop_unavailable") {
    return (
      <AccountNotice
        description={translate("states.notOfferedDescription")}
        title={translate("states.notOffered")}
        variant="offline"
      />
    );
  }
  if (code === "maintenance_active") {
    return (
      <AccountNotice
        description={translate("states.maintenanceDescription")}
        title={translate("states.maintenance")}
        variant="offline"
      />
    );
  }
  if (code === "price_unavailable") {
    return (
      <AccountNotice
        action={<Button onClick={onRetry}>{translate("actions.retry")}</Button>}
        description={translate("quote.unavailableDescription")}
        title={translate("quote.unavailable")}
        variant="offline"
      />
    );
  }
  return (
    <AccountNotice
      action={<Button onClick={onRetry}>{translate("actions.retry")}</Button>}
      description={translate("states.errorDescription")}
      title={translate("states.error")}
      variant="danger"
    />
  );
}
