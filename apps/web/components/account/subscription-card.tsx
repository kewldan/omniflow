"use client";

import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import { ArrowUpDown, KeyRound, Receipt, ShoppingBag } from "lucide-react";
import Link from "next/link";
import { useFormatter, useTranslations } from "next-intl";

/** Mirrors the AccountSubscription schema in api/openapi.yaml. */
export type AccountSubscription = {
  id: string;
  slot: number;
  label: string;
  plan: string;
  phase:
    | "none"
    | "provisioning"
    | "active"
    | "expiring_soon"
    | "grace"
    | "limited"
    | "disabled"
    | "paused"
    | "expired"
    | "failed";
  endsAt?: string;
  daysLeft: number;
  provisioned: boolean;
  live: boolean;
  traffic: { usedBytes: number; limitBytes?: number | null; unlimited: boolean; percent: number };
  devices: { used: number; limit?: number | null; unlimited: boolean };
  /** The unpaid order that opened this subscription, while nothing has been provisioned. */
  pendingOrderId?: string;
};

/**
 * What a subscription's primary control should be.
 *
 * "Connect" is the right answer only for a subscription that has been
 * provisioned and is usable. A card for an unpaid purchase led to a connect
 * screen that answered 409; an expired one led to a link that no longer
 * worked, with no way to the store from the place the customer was looking.
 */
export function primaryAction(
  subscription: AccountSubscription,
): "pay" | "connect" | "renew" | "browse" {
  if (subscription.pendingOrderId) {
    return "pay";
  }
  if (!subscription.provisioned) {
    return "browse";
  }
  switch (subscription.phase) {
    case "expired":
    case "disabled":
    case "none":
    case "failed":
      return "renew";
    default:
      return "connect";
  }
}

/** The phase sentence, with "payment pending" taking precedence over "not active". */
export function usePhaseLabel() {
  const translate = useTranslations("account");
  const format = useFormatter();
  return (subscription: AccountSubscription) => {
    if (subscription.pendingOrderId) {
      return translate("subscription.phase.payment_pending");
    }
    return subscription.endsAt
      ? translate(`subscription.phase.${subscription.phase}`, {
          date: format.dateTime(new Date(subscription.endsAt), {
            day: "numeric",
            month: "long",
            year: "numeric",
          }),
        })
      : translate(`subscription.phase.${subscription.phase}`, { date: "" });
  };
}

/**
 * Which tone a phase renders in.
 *
 * The mapping lives here rather than in each screen so the dot beside a name and
 * the sentence under it can never disagree about how bad a state is.
 */
const PHASE_TONE: Record<AccountSubscription["phase"], "ok" | "warn" | "bad"> = {
  active: "ok",
  disabled: "bad",
  expired: "bad",
  expiring_soon: "warn",
  failed: "bad",
  grace: "warn",
  limited: "warn",
  // Warn rather than bad: a paused subscription is not working *and* nothing is
  // being lost, which is a different thing from expired and reads differently.
  paused: "warn",
  none: "bad",
  provisioning: "warn",
};

const TONE_CLASS = {
  bad: "text-destructive",
  ok: "text-foreground",
  warn: "text-warning",
} as const;

const DOT_CLASS = {
  bad: "bg-destructive",
  ok: "bg-primary",
  warn: "bg-warning",
} as const;

/** Renders a byte count the way the customer reads it, not as a raw integer. */
export function useByteFormatter() {
  const format = useFormatter();
  return (bytes: number) => {
    const gigabytes = bytes / 1024 ** 3;
    if (gigabytes >= 1024) {
      return `${format.number(gigabytes / 1024, { maximumFractionDigits: 2 })} TB`;
    }
    if (gigabytes >= 1) {
      return `${format.number(gigabytes, { maximumFractionDigits: 1 })} GB`;
    }
    return `${format.number(bytes / 1024 ** 2, { maximumFractionDigits: 0 })} MB`;
  };
}

/**
 * The traffic bar and the sentence that says the same thing in words.
 *
 * The textual equivalent is not a tooltip or a visually-hidden label bolted on
 * afterwards: it is the line above the bar, visible to everyone. The bar carries
 * `role="img"` with that same sentence as its accessible name, so a screen
 * reader gets one clear statement rather than a progressbar reading out a bare
 * percentage that means nothing without its ceiling.
 */
export function TrafficMeter({ traffic }: { traffic: AccountSubscription["traffic"] }) {
  const translate = useTranslations("account");
  const formatBytes = useByteFormatter();

  const summary = traffic.unlimited
    ? translate("subscription.trafficUnlimited", { used: formatBytes(traffic.usedBytes) })
    : translate("subscription.trafficUsed", {
        limit: formatBytes(traffic.limitBytes ?? 0),
        used: formatBytes(traffic.usedBytes),
      });

  return (
    <div className="space-y-2">
      <p className="font-mono text-[11px] text-muted-foreground">{summary}</p>
      {!traffic.unlimited && (
        <div aria-label={summary} className="h-1 overflow-hidden rounded-full bg-muted" role="img">
          <div
            className={cn(
              "h-full rounded-full transition-[width] duration-700 ease-emphasis motion-reduce:transition-none",
              traffic.percent >= 100 ? "bg-destructive" : "bg-primary",
            )}
            style={{ width: `${traffic.percent}%` }}
          />
        </div>
      )}
    </div>
  );
}

/**
 * The state a subscription is in, in words, with the days left beside it.
 *
 * Shared between the dashboard card and the subscription page so the two can
 * never disagree. The detail page used to show neither: a customer who opened it
 * to find out when their subscription ends was told its name and its traffic and
 * left to guess the rest.
 */
export function SubscriptionStatus({ subscription }: { subscription: AccountSubscription }) {
  const translate = useTranslations("account");
  const phaseLabel = usePhaseLabel();
  const tone = subscription.pendingOrderId ? "warn" : PHASE_TONE[subscription.phase];

  const status = phaseLabel(subscription);

  const devices = subscription.devices.unlimited
    ? translate("subscription.devicesUnlimited", { used: subscription.devices.used })
    : translate("subscription.devicesUsed", {
        limit: subscription.devices.limit ?? 0,
        used: subscription.devices.used,
      });

  return (
    <div className="flex items-start justify-between gap-3">
      <div className="min-w-0 space-y-1.5">
        <p className={cn("flex items-center gap-2 font-mono text-[11.5px]", TONE_CLASS[tone])}>
          <span aria-hidden className={cn("size-[7px] shrink-0 rounded-full", DOT_CLASS[tone])} />
          {status}
        </p>
        <p className="font-mono text-[11px] text-subtle-foreground">{devices}</p>
      </div>
      <div className="shrink-0 text-right">
        <div className="font-bold text-[26px] leading-none tracking-[-0.04em]" data-numeric>
          {subscription.daysLeft}
        </div>
        <div className="mt-1 font-mono text-[10px] text-subtle-foreground">
          {translate("subscription.days")}
        </div>
      </div>
    </div>
  );
}

/**
 * One subscription, as the dashboard lists it.
 *
 * The remaining-days figure is the headline because it is the question the
 * customer opened the page to answer. Everything else supports it.
 */
export function SubscriptionCard({ subscription }: { subscription: AccountSubscription }) {
  const translate = useTranslations("account");
  const phaseLabel = usePhaseLabel();
  const tone = subscription.pendingOrderId ? "warn" : PHASE_TONE[subscription.phase];

  const status = phaseLabel(subscription);
  const action = primaryAction(subscription);

  const devices = subscription.devices.unlimited
    ? translate("subscription.devicesUnlimited", { used: subscription.devices.used })
    : translate("subscription.devicesUsed", {
        limit: subscription.devices.limit ?? 0,
        used: subscription.devices.used,
      });

  return (
    <article className="animate-step-in space-y-4 rounded-lg border border-border bg-card p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span aria-hidden className={cn("size-[7px] shrink-0 rounded-full", DOT_CLASS[tone])} />
            <h3 className="truncate font-semibold text-[17px] tracking-[-0.01em]">
              {subscription.label}
            </h3>
          </div>
          <p className={cn("mt-1.5 font-mono text-[11.5px]", TONE_CLASS[tone])}>{status}</p>
          {subscription.plan && (
            <p className="mt-1 font-mono text-[11px] text-subtle-foreground">{subscription.plan}</p>
          )}
        </div>
        <div className="shrink-0 text-right">
          <div className="font-bold text-[26px] leading-none tracking-[-0.04em]" data-numeric>
            {subscription.daysLeft}
          </div>
          <div className="mt-1 font-mono text-[10px] text-subtle-foreground">
            {translate("subscription.days")}
          </div>
        </div>
      </div>

      <div className="space-y-2">
        <p className="font-mono text-[11px] text-muted-foreground">{devices}</p>
        <TrafficMeter traffic={subscription.traffic} />
      </div>

      <div className="flex gap-2">
        {action === "pay" && (
          <Button asChild className="flex-1" size="lg">
            <Link href={`/account/orders/${subscription.pendingOrderId}`}>
              <Receipt aria-hidden />
              {translate("subscription.openOrder")}
            </Link>
          </Button>
        )}
        {action === "connect" && (
          <Button asChild className="flex-1" size="lg">
            <Link href={`/account/subscriptions/${subscription.id}/connect`}>
              <KeyRound aria-hidden />
              {translate("subscription.connect")}
            </Link>
          </Button>
        )}
        {(action === "renew" || action === "browse") && (
          <Button asChild className="flex-1" size="lg">
            <Link href="/account/store">
              <ShoppingBag aria-hidden />
              {translate(action === "renew" ? "subscription.renew" : "subscription.browsePlans")}
            </Link>
          </Button>
        )}
        <Button asChild size="lg" variant="outline">
          <Link
            aria-label={translate("subscription.manage")}
            href={`/account/subscriptions/${subscription.id}`}
          >
            <ArrowUpDown aria-hidden />
          </Link>
        </Button>
      </div>
    </article>
  );
}
