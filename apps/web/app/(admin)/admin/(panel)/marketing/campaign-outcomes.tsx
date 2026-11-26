"use client";

import { Button } from "@omniflow/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@omniflow/ui/popover";
import { Skeleton } from "@omniflow/ui/skeleton";
import { useLocale, useTranslations } from "next-intl";
import { useState } from "react";
import useSWR from "swr";
import { type ApiError, fetcher } from "@/lib/api";

/** One group of a campaign's recipients: a status and why they ended in it. */
type Outcome = { status: string; reason?: string; errorCode?: string; recipients: number };

/** The groups the breakdown shows, in reading order. */
const GROUPS = ["sent", "failed", "suppressed", "queued"] as const;
type Group = (typeof GROUPS)[number];

/** Telegram-blocked deliveries count as failures, as the campaign totals do. */
function groupOf(status: string): Group | undefined {
  if (status === "blocked") {
    return "failed";
  }
  return (GROUPS as readonly string[]).includes(status) ? (status as Group) : undefined;
}

const KNOWN_REASONS = new Set([
  "no_consent",
  "suppressed",
  "frequency_cap",
  "quiet_hours",
  "delivery_blocked",
  "no_telegram",
  "no_email",
]);
const KNOWN_ERRORS = new Set([
  "send_failed",
  "classification_failed",
  "account_inactive",
  "email_rejected",
  "email_exhausted",
  "email_unreadable",
]);

/**
 * Why a campaign's recipients were sent, skipped, or failed, read when the
 * operator opens it. The table row keeps its three totals; this explains them
 * — "400 skipped" is a decision when it is quiet hours and a problem when it
 * is a blocked bot, and the totals alone cannot say which. Counts only: no
 * recipient is named.
 */
export function CampaignOutcomes({ campaignId }: { campaignId: string }) {
  const translate = useTranslations("admin.marketing.campaigns.breakdown");
  const locale = useLocale();
  const [open, setOpen] = useState(false);
  const { data, error, isLoading } = useSWR<{ items: Outcome[] }, ApiError>(
    open ? `/v1/panel/marketing/campaigns/${campaignId}/outcomes` : null,
    fetcher,
  );

  const label = (outcome: Outcome): string => {
    const parts: string[] = [];
    if (outcome.reason) {
      parts.push(
        KNOWN_REASONS.has(outcome.reason) ? translate(`reasons.${outcome.reason}`) : outcome.reason,
      );
    }
    if (outcome.errorCode) {
      parts.push(
        KNOWN_ERRORS.has(outcome.errorCode)
          ? translate(`errors.${outcome.errorCode}`)
          : outcome.errorCode,
      );
    }
    return parts.length > 0 ? parts.join(" · ") : translate("noReason");
  };

  const grouped = GROUPS.map((group) => {
    const rows = (data?.items ?? []).filter((outcome) => groupOf(outcome.status) === group);
    return { group, rows, total: rows.reduce((sum, row) => sum + row.recipients, 0) };
  }).filter((entry) => entry.total > 0);

  return (
    <Popover onOpenChange={setOpen} open={open}>
      <PopoverTrigger asChild>
        <Button className="h-auto px-1 py-0 text-xs" size="sm" type="button" variant="link">
          {translate("open")}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 text-left">
        <p className="mb-2 font-medium text-sm">{translate("title")}</p>
        {isLoading ? (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-4 w-full" />
            <Skeleton className="h-4 w-2/3" />
          </div>
        ) : error ? (
          <p className="text-danger-foreground text-sm" role="alert">
            {translate("failed")}
          </p>
        ) : grouped.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("empty")}</p>
        ) : (
          <dl className="flex flex-col gap-3 text-sm">
            {grouped.map(({ group, rows, total }) => (
              <div key={group}>
                <dt className="flex justify-between font-medium">
                  <span>{translate(`groups.${group}`)}</span>
                  <span className="tabular-nums">{total.toLocaleString(locale)}</span>
                </dt>
                {group === "failed" || group === "suppressed"
                  ? rows.map((row) => (
                      <dd
                        className="flex justify-between gap-3 text-muted-foreground"
                        key={`${row.status}:${row.reason ?? ""}:${row.errorCode ?? ""}`}
                      >
                        <span>{label(row)}</span>
                        <span className="tabular-nums">
                          {row.recipients.toLocaleString(locale)}
                        </span>
                      </dd>
                    ))
                  : null}
              </div>
            ))}
          </dl>
        )}
      </PopoverContent>
    </Popover>
  );
}
