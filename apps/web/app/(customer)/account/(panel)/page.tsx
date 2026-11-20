"use client";

import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import Link from "next/link";
import { useFormatter, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import {
  AccountNotice,
  ListSkeleton,
  SectionLabel,
  ServiceNotice,
  StaleDataNotice,
} from "@/components/account/state";
import { type AccountSubscription, SubscriptionCard } from "@/components/account/subscription-card";
import { type ApiError, fetcher } from "@/lib/api";

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
 */
export default function AccountDashboard() {
  const translate = useTranslations("account");
  const { data, error, isLoading, isValidating } = useSWR<Overview, ApiError>(
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

      {data.showSwitcher && data.subscriptions.length > 1 && (
        <SubscriptionSwitcher
          onSelect={setSelected}
          selected={selected}
          subscriptions={data.subscriptions}
        />
      )}

      <div className="flex items-baseline justify-between">
        <SectionLabel>{translate("dashboard.subscriptions")}</SectionLabel>
        <span className="font-medium font-mono text-[10px] text-subtle-foreground" data-numeric>
          {data.subscriptions.length}
        </span>
      </div>

      {/* aria-busy lets assistive technology know a background refresh is in
          flight without interrupting whatever it is reading. */}
      <section aria-busy={isValidating} className="space-y-3">
        {active.map((subscription) => (
          <SubscriptionCard key={subscription.id} subscription={subscription} />
        ))}
      </section>
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
