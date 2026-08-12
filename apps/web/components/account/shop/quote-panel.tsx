"use client";

import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import { TimerReset } from "lucide-react";
import { useTranslations } from "next-intl";
import { useEffect, useState } from "react";

import type { ShopQuote } from "@/components/account/shop/types";
import { useDuration, useMoney } from "@/lib/format";

/** Renders a remaining second count as m:ss, which is how a hold reads. */
function clock(seconds: number): string {
  const minutes = Math.floor(seconds / 60);
  return `${minutes}:${String(seconds % 60).padStart(2, "0")}`;
}

/**
 * The live hold on the displayed price.
 *
 * The ticking numerals are hidden from assistive technology and paired with a
 * static sentence instead. A region that re-announced itself every second would
 * make the page unusable with a screen reader, while the fact that matters —
 * this price is held briefly and will refresh itself — does not change from
 * second to second.
 *
 * When the hold lapses the component says so and asks its parent for a new
 * quote. It never lets the moment pass silently: a submitted stale price is
 * refused by the server, and a customer who was shown a number that had quietly
 * stopped applying has been misled even if nothing was charged.
 */
export function QuoteCountdown({
  expiresAt,
  onExpire,
}: {
  expiresAt: string;
  onExpire: () => void;
}) {
  const translate = useTranslations("account.shop");
  const duration = useDuration();
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);

  const { expired } = duration(expiresAt, new Date(now));
  const remaining = Math.max(0, Math.round((new Date(expiresAt).getTime() - now) / 1000));

  // The dependency list carries `expired` rather than `now`, so this fires once
  // when the hold lapses and again only after a fresh quote has lapsed in turn.
  useEffect(() => {
    if (expired) {
      onExpire();
    }
  }, [expired, onExpire]);

  if (expired) {
    return (
      <p className="flex items-center gap-1.5 font-mono text-[11px] text-warning" role="status">
        <TimerReset aria-hidden className="size-3.5" />
        {translate("quote.refreshing")}
      </p>
    );
  }

  return (
    <p className="flex items-center gap-1.5 font-mono text-[11px] text-subtle-foreground">
      <TimerReset aria-hidden className="size-3.5" />
      <span aria-hidden>{translate("quote.holdFor", { time: clock(remaining) })}</span>
      <span className="sr-only">{translate("quote.holdNotice")}</span>
    </p>
  );
}

/**
 * The money on the purchase screen: what it costs, what a code took off, and
 * what will actually be charged.
 *
 * The total is a separate line rather than the only line, because a customer
 * who typed a promo code needs to see it doing something. The price above it is
 * the quote the server will be handed back verbatim.
 */
export function QuoteSummary({
  discountMinor,
  quote,
}: {
  discountMinor: number;
  quote: ShopQuote;
}) {
  const translate = useTranslations("account.shop");
  const money = useMoney();
  const total = Math.max(0, quote.priceMinor - discountMinor);

  return (
    <dl className="space-y-1.5">
      <Row label={translate("product.price")} value={money(quote.priceMinor, quote.currency)} />
      {discountMinor > 0 && (
        <Row
          label={translate("product.discount")}
          tone="good"
          value={`−${money(discountMinor, quote.currency)}`}
        />
      )}
      <Row emphasis label={translate("product.total")} value={money(total, quote.currency)} />
    </dl>
  );
}

function Row({
  emphasis,
  label,
  tone,
  value,
}: {
  emphasis?: boolean;
  label: string;
  tone?: "good";
  value: string;
}) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt
        className={cn(
          "text-[12.5px]",
          emphasis ? "font-medium text-foreground" : "text-muted-foreground",
        )}
      >
        {label}
      </dt>
      <dd
        className={cn(
          "font-mono",
          emphasis ? "font-semibold text-[15px]" : "text-[12.5px]",
          tone === "good" && "text-success",
        )}
        data-numeric
      >
        {value}
      </dd>
    </div>
  );
}

/**
 * The price moved between the customer agreeing to a number and the order being
 * opened.
 *
 * Both numbers are shown together because the customer's decision is a
 * comparison, and the new one is never charged by simply appearing: buying at
 * it is a separate press. Nothing has been taken at this point, and the copy
 * says so, since "the price changed" otherwise reads as though something
 * already went wrong with a payment.
 */
export function PriceChangeNotice({
  after,
  before,
  busy,
  onAccept,
  onDismiss,
}: {
  after: ShopQuote;
  before: ShopQuote;
  busy: boolean;
  onAccept: () => void;
  onDismiss: () => void;
}) {
  const translate = useTranslations("account.shop");
  const money = useMoney();

  return (
    <section
      aria-label={translate("quote.changed.title")}
      className="space-y-3 rounded-lg border border-warning/40 bg-warning/10 p-4"
      role="alert"
    >
      <div className="space-y-1">
        <p className="font-semibold text-[13.5px]">{translate("quote.changed.title")}</p>
        <p className="text-[12.5px] leading-relaxed">{translate("quote.changed.description")}</p>
      </div>

      <div className="grid grid-cols-2 gap-3">
        <div className="rounded-md border border-border bg-card px-3 py-2">
          <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
            {translate("quote.changed.before")}
          </p>
          <p className="mt-1 font-mono text-[14px] line-through" data-numeric>
            {money(before.priceMinor, before.currency)}
          </p>
        </div>
        <div className="rounded-md border border-primary/40 bg-card px-3 py-2">
          <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
            {translate("quote.changed.after")}
          </p>
          <p className="mt-1 font-mono font-semibold text-[14px]" data-numeric>
            {money(after.priceMinor, after.currency)}
          </p>
        </div>
      </div>

      <div className="flex gap-2">
        <Button className="flex-1" disabled={busy} onClick={onAccept} size="lg">
          {translate("quote.changed.accept")}
        </Button>
        <Button disabled={busy} onClick={onDismiss} size="lg" variant="outline">
          {translate("actions.cancel")}
        </Button>
      </div>
    </section>
  );
}
