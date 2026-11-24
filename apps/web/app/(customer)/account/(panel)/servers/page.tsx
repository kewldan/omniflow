"use client";

import { ChevronRight } from "lucide-react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { useEffect } from "react";
import useSWR from "swr";

import { AccountNotice, ListSkeleton } from "@/components/account/state";
import type { AccountSubscription } from "@/components/account/subscription-card";
import { type ApiError, fetcher } from "@/lib/api";

/**
 * The menu's way into servers and routing. The screen belongs to one
 * subscription, so this sends a customer with one straight to it and asks a
 * customer with several which one.
 */
export default function ServersEntryPage() {
  const translate = useTranslations("account.smart");
  const router = useRouter();
  const { data, error, isLoading } = useSWR<{ subscriptions: AccountSubscription[] }, ApiError>(
    "/v1/account/overview",
    fetcher,
  );
  const ready = (data?.subscriptions ?? []).filter((subscription) => subscription.provisioned);
  const only = ready.length === 1 ? ready[0] : undefined;

  useEffect(() => {
    if (only) {
      router.replace(`/account/subscriptions/${only.id}/servers`);
    }
  }, [only, router]);

  if (isLoading || only) {
    return <ListSkeleton rows={3} />;
  }
  if (error) {
    return <AccountNotice title={translate("pickError")} variant="danger" />;
  }
  if (ready.length === 0) {
    return (
      <AccountNotice
        description={translate("pickNoneDescription")}
        title={translate("pickNone")}
        variant="empty"
      />
    );
  }
  return (
    <div className="animate-step-in space-y-4">
      <h1 className="font-semibold text-[19px] tracking-[-0.02em]">{translate("pickTitle")}</h1>
      <ul className="space-y-2">
        {ready.map((subscription) => (
          <li key={subscription.id}>
            <Link
              className="flex items-center gap-3 rounded-lg border border-border bg-card p-4 transition-colors hover:border-foreground/20"
              href={`/account/subscriptions/${subscription.id}/servers`}
            >
              <span className="min-w-0 flex-1">
                <span className="block truncate font-medium text-[14px]">
                  {subscription.label || subscription.plan}
                </span>
                <span className="block truncate text-[12.5px] text-muted-foreground">
                  {subscription.plan}
                </span>
              </span>
              <ChevronRight aria-hidden className="size-4 text-subtle-foreground" />
            </Link>
          </li>
        ))}
      </ul>
    </div>
  );
}
