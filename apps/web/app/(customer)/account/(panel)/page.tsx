"use client";

import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import Link from "next/link";
import { useFormatter, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";
import { DeviceList } from "@/components/account/device-list";
import {
  AccountNotice,
  ListSkeleton,
  SectionLabel,
  ServiceNotice,
  StaleDataNotice,
} from "@/components/account/state";
import {
  type AccountSubscription,
  primaryAction,
  SubscriptionCard,
  SubscriptionHero,
} from "@/components/account/subscription-card";
import { type ApiError, apiFetch, fetcher } from "@/lib/api";

type Overview = {
  customer: { id: string; locale: "en" | "ru"; timezone: string; status: string };
  subscriptions: AccountSubscription[];
  showSwitcher: boolean;
  degraded: boolean;
  /** Present only while maintenance or an incident is active. */
  notice?: { active: boolean; message?: string; expectedReturnAt?: string };
  /** A subscription that ended recently, with the customer's best live discount. */
  recovery?: {
    subscriptionId: string;
    label: string;
    endedAt: string;
    offer?: { kind: "percent" | "fixed"; value: number; expiresAt: string };
  };
};

/**
 * The dashboard.
 *
 * One screen answers the question the customer came with: is my subscription
 * working, and how long is left. Everything else on the page is reachable from
 * here rather than competing with it.
 *
 * Most customers have exactly one subscription, so when one is on screen —
 * the only one, or the one picked in the switcher — the dashboard opens it out:
 * status, expiry, traffic, and its devices with removal, without a second
 * screen. Several at once stay compact cards.
 */
export default function AccountDashboard() {
  const translate = useTranslations("account");
  const { data, error, isLoading, isValidating, mutate } = useSWR<Overview, ApiError>(
    "/v1/account/overview",
    fetcher,
  );
  const [selected, setSelected] = useState<string | null>(null);

  if (isLoading) {
    return <ListSkeleton />;
  }
  if (error) {
    return (
      <AccountNotice
        description={translate("states.errorDescription")}
        title={translate("states.error")}
        variant="danger"
      />
    );
  }
  if (!data || data.subscriptions.length === 0) {
    return (
      <div className="space-y-4">
        {/* A maintenance window matters most to somebody about to buy: purchases
            are paused during one, and a new customer used to find that out only
            from the refusal at the end of checkout. */}
        {data?.notice?.active && (
          <ServiceNotice
            expectedReturnAt={data.notice.expectedReturnAt}
            message={data.notice.message}
          />
        )}
        {/* The one thing somebody without a subscription can do here is get
            one, so the empty state offers it rather than describing it and
            leaving them to find the store in the tab bar. */}
        <AccountNotice
          action={
            <Button asChild>
              <Link href="/account/store">{translate("dashboard.emptyAction")}</Link>
            </Button>
          }
          description={translate("dashboard.emptyDescription")}
          title={translate("dashboard.empty")}
        />
      </div>
    );
  }

  // The switcher appears only when the installation allows concurrent
  // subscriptions. A single-subscription installation gets exactly the one-screen
  // experience it had before, with no selection step to dismiss.
  const active =
    data.showSwitcher && selected
      ? data.subscriptions.filter((subscription) => subscription.id === selected)
      : data.subscriptions;

  return (
    <div className="space-y-4">
      {data.notice?.active && (
        <ServiceNotice
          expectedReturnAt={data.notice.expectedReturnAt}
          message={data.notice.message}
        />
      )}
      {data.degraded && <StaleDataNotice />}
      {data.recovery && <RecoveryCard recovery={data.recovery} />}
      <ReminderCards />

      {data.showSwitcher && data.subscriptions.length > 1 && (
        <SubscriptionSwitcher
          onSelect={setSelected}
          selected={selected}
          subscriptions={data.subscriptions}
        />
      )}

      {/* With one subscription the page heading already says what this is;
          the section label and its count of one only repeated it. */}
      {data.subscriptions.length > 1 && (
        <div className="flex items-baseline justify-between">
          <SectionLabel>{translate("dashboard.subscriptions")}</SectionLabel>
          <span className="font-medium text-[12px] text-subtle-foreground" data-numeric>
            {data.subscriptions.length}
          </span>
        </div>
      )}

      {/* aria-busy lets assistive technology know a background refresh is in
          flight without interrupting whatever it is reading. */}
      <section aria-busy={isValidating} className="space-y-3">
        {active.length === 1 ? (
          <SubscriptionHome
            degraded={data.degraded}
            onDevicesChanged={() => void mutate()}
            subscription={active[0]}
          />
        ) : (
          active.map((subscription) => (
            <SubscriptionCard key={subscription.id} subscription={subscription} />
          ))
        )}
      </section>
    </div>
  );
}

/**
 * A single subscription, opened out.
 *
 * The subscription as one card with its days left as the headline, followed by
 * its devices. The device list is shown only for
 * a subscription that can be connected: an unpaid, lapsed, or unprovisioned one
 * has no devices to manage, and its card already says what to do instead. It is
 * left out while Remnawave is unreachable too — the stale-data notice above
 * already says so, and a second notice in the list would repeat it.
 */
function SubscriptionHome({
  degraded,
  onDevicesChanged,
  subscription,
}: {
  degraded: boolean;
  onDevicesChanged: () => void;
  subscription: AccountSubscription;
}) {
  return (
    <div className="space-y-5">
      <SubscriptionHero subscription={subscription} />
      {primaryAction(subscription) === "connect" && !degraded && (
        <DeviceList embedded onChanged={onDevicesChanged} subscription={subscription} />
      )}
    </div>
  );
}

/**
 * The subscription selector.
 *
 * It is a group of toggle buttons rather than a select: there are rarely more
 * than a handful, and seeing all of them at once with their state is the point.
 */
function SubscriptionSwitcher({
  onSelect,
  selected,
  subscriptions,
}: {
  onSelect: (id: string | null) => void;
  selected: string | null;
  subscriptions: AccountSubscription[];
}) {
  const translate = useTranslations("account");
  return (
    <fieldset className="flex flex-wrap gap-2">
      <legend className="sr-only">{translate("dashboard.switcher")}</legend>
      <Button
        aria-pressed={selected === null}
        className={cn(selected === null && "border-primary")}
        onClick={() => onSelect(null)}
        size="sm"
        variant={selected === null ? "secondary" : "outline"}
      >
        {translate("dashboard.all")}
      </Button>
      {subscriptions.map((subscription) => (
        <Button
          aria-pressed={selected === subscription.id}
          className={cn(selected === subscription.id && "border-primary")}
          key={subscription.id}
          onClick={() => onSelect(subscription.id)}
          size="sm"
          variant={selected === subscription.id ? "secondary" : "outline"}
        >
          {subscription.label}
        </Button>
      ))}
    </fieldset>
  );
}

/**
 * Brings back a subscription that ended recently — the web side of the bot's
 * recovery message. It names the discount waiting for the customer, which the
 * checkout applies by itself, so there is no code to copy.
 */
function RecoveryCard({ recovery }: { recovery: NonNullable<Overview["recovery"]> }) {
  const translate = useTranslations("account.recovery");
  const format = useFormatter();
  return (
    <div className="space-y-3 rounded-xl border border-primary/40 bg-card p-4">
      <p className="font-semibold text-[15px]">{translate("title", { label: recovery.label })}</p>
      <p className="text-[13px] text-muted-foreground leading-relaxed">
        {translate("description", { ended: format.relativeTime(new Date(recovery.endedAt)) })}
      </p>
      {recovery.offer && recovery.offer.kind === "percent" ? (
        <p className="font-medium text-[13px] text-primary">
          {translate("offer", {
            percent: recovery.offer.value,
            until: format.dateTime(new Date(recovery.offer.expiresAt), {
              day: "numeric",
              hour: "2-digit",
              minute: "2-digit",
              month: "short",
            }),
          })}
        </p>
      ) : null}
      <Button asChild className="w-full" size="lg">
        <Link href="/account/store">{translate("action")}</Link>
      </Button>
    </div>
  );
}

const REMINDER_LINKS: Record<string, string> = {
  plans: "/account/store",
  connect: "/account/devices",
  wallet: "/account/wallet",
  support: "/account/support",
  home: "/account",
};

/**
 * The operator's reminders that hold for this customer now, as cards they can
 * act on or dismiss. Dismissing one also stops its messages in the bot.
 */
function ReminderCards() {
  const translate = useTranslations("account.reminders");
  const { data, mutate } = useSWR<
    { items: { id: string; title: string; body: string; route?: string }[] },
    ApiError
  >("/v1/account/reminders", fetcher);

  async function dismiss(id: string) {
    try {
      await apiFetch(`/v1/account/reminders/${id}/dismiss`, { method: "POST" });
      await mutate();
    } catch {
      // A card that stays is harmless; it is dismissed on the next try.
    }
  }

  return (
    <>
      {(data?.items ?? []).map((card) => (
        <div className="space-y-2 rounded-xl border border-border bg-card p-4" key={card.id}>
          <p className="font-semibold text-[15px]">{card.title}</p>
          <p className="whitespace-pre-line text-[13px] text-muted-foreground leading-relaxed">
            {card.body}
          </p>
          <div className="flex gap-2">
            {card.route && REMINDER_LINKS[card.route] ? (
              <Button asChild size="sm">
                <Link href={REMINDER_LINKS[card.route]}>{translate("open")}</Link>
              </Button>
            ) : null}
            <Button onClick={() => dismiss(card.id)} size="sm" variant="ghost">
              {translate("dismiss")}
            </Button>
          </div>
        </div>
      ))}
    </>
  );
}
