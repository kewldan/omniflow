"use client";

import { Button } from "@omniflow/ui/button";
import { cn } from "@omniflow/ui/lib/utils";
import { useTranslations } from "next-intl";
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
      <AccountNotice
        description={translate("dashboard.emptyDescription")}
        title={translate("dashboard.empty")}
      />
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
