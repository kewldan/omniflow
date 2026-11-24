"use client";

import { ChevronRight, Globe } from "lucide-react";
import Link from "next/link";
import { useTranslations } from "next-intl";

import { useSmartEnabled } from "@/lib/smart-enabled";

/**
 * The way in from a subscription's page. It renders nothing while the
 * operator has the module off, or for a subscription not yet set up.
 */
export function SmartSubscriptionEntry({
  provisioned,
  subscriptionId,
}: {
  provisioned: boolean;
  subscriptionId: string;
}) {
  const translate = useTranslations("account.smart");
  const enabled = useSmartEnabled();
  if (!enabled || !provisioned) {
    return null;
  }
  return (
    <Link
      className="flex items-center gap-3 rounded-lg border border-border bg-card p-4 transition-colors hover:border-foreground/20"
      href={`/account/subscriptions/${subscriptionId}/servers`}
    >
      <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-secondary">
        <Globe aria-hidden className="size-[18px]" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block font-medium text-[14px]">{translate("entryTitle")}</span>
        <span className="block text-[12.5px] text-muted-foreground">
          {translate("entryDescription")}
        </span>
      </span>
      <ChevronRight aria-hidden className="size-4 text-subtle-foreground" />
    </Link>
  );
}
