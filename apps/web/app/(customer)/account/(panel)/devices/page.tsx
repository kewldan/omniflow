"use client";

import { Button } from "@omniflow/ui/button";
import { useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";

import { DeviceList } from "@/components/account/device-list";
import { AccountNotice, ListSkeleton } from "@/components/account/state";
import type { AccountSubscription } from "@/components/account/subscription-card";
import { type ApiError, fetcher } from "@/lib/api";

type Overview = { subscriptions: AccountSubscription[]; showSwitcher: boolean };

/**
 * The devices screen.
 *
 * Devices belong to a subscription rather than to the account, so this picks one
 * — the only one, in a single-subscription installation — and lets the customer
 * change which when there are several.
 */
export default function DevicesPage() {
  const translate = useTranslations("account");
  const { data: overview, isLoading: loadingOverview } = useSWR<Overview, ApiError>(
    "/v1/account/overview",
    fetcher,
  );
  const [subscriptionId, setSubscriptionId] = useState<string | null>(null);

  const subscriptions = overview?.subscriptions ?? [];
  const active = subscriptions.find((item) => item.id === subscriptionId) ?? subscriptions[0];

  if (loadingOverview) {
    return <ListSkeleton />;
  }
  if (!active) {
    return (
      <AccountNotice
        description={translate("dashboard.emptyDescription")}
        title={translate("dashboard.empty")}
      />
    );
  }

  return (
    <div className="space-y-4">
      {subscriptions.length > 1 && (
        <fieldset className="flex flex-wrap gap-2">
          <legend className="sr-only">{translate("dashboard.switcher")}</legend>
          {subscriptions.map((subscription) => (
            <Button
              aria-pressed={subscription.id === active.id}
              key={subscription.id}
              onClick={() => setSubscriptionId(subscription.id)}
              size="sm"
              variant={subscription.id === active.id ? "secondary" : "outline"}
            >
              {subscription.label}
            </Button>
          ))}
        </fieldset>
      )}
      <DeviceList subscription={active} />
    </div>
  );
}
