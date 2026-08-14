"use client";

import { useFormatter, useTranslations } from "next-intl";
import useSWR from "swr";

import { type ApiError, fetcher } from "@/lib/api";

import { NOTIFICATION_HISTORY_KEY, type NotificationDelivery } from "./types";

/**
 * What was actually sent.
 *
 * The switches above this say what should arrive. That is a setting, not
 * evidence, and it leaves "I never got the expiry warning" as a claim the
 * customer cannot check — they have only the absence of a message to go on, and
 * an absence looks identical whether the bot is broken, the message was held
 * back by a quiet window, or nothing was ever due.
 *
 * So this sits directly beneath the settings that produced it. The reason on a
 * message that did not go out is the useful half: `quiet_hours` and
 * `frequency_cap` and `no_consent` all point back at a control on this same
 * page, which turns "nothing arrived" into something the customer can act on
 * without opening a ticket.
 *
 * No message bodies. The record says a notice of a kind happened, not what it
 * said, and rendering a template against today's data would show the customer
 * something that was never sent to them.
 */
export function DeliveryHistory() {
  const translate = useTranslations("account.support");
  const format = useFormatter();
  const { data, error, isLoading } = useSWR<{ items: NotificationDelivery[] }, ApiError>(
    NOTIFICATION_HISTORY_KEY,
    fetcher,
  );

  // A history that will not load is not worth an error banner on a screen whose
  // job is the settings above it. The customer still has every control; they
  // have lost a record, and saying so quietly is the proportionate response.
  if (isLoading || error) {
    return null;
  }

  const items = data?.items ?? [];
  if (items.length === 0) {
    return (
      <p className="rounded-xl border border-border bg-card px-4 py-3 text-[12.5px] text-muted-foreground">
        {translate("history.empty")}
      </p>
    );
  }

  return (
    <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
      {items.map((delivery) => (
        <li
          className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 px-4 py-3"
          key={`${delivery.kind}-${delivery.scheduledAt}-${delivery.subscriptionSlot ?? 0}`}
        >
          <span className="flex flex-col">
            <span className="font-medium text-[13.5px]">
              {label(translate, "history.kind", delivery.kind)}
              {delivery.subscriptionLabel ? (
                <span className="ml-2 font-normal text-[12px] text-muted-foreground">
                  {delivery.subscriptionLabel}
                </span>
              ) : null}
            </span>
            <span className="text-[12px] text-muted-foreground">
              {delivery.status === "sent"
                ? translate("history.sent")
                : label(translate, "history.why", delivery.reason ?? delivery.status)}
            </span>
          </span>
          <time
            className="font-mono text-[11px] text-muted-foreground"
            dateTime={delivery.sentAt ?? delivery.scheduledAt}
          >
            {format.dateTime(new Date(delivery.sentAt ?? delivery.scheduledAt), {
              day: "numeric",
              hour: "2-digit",
              minute: "2-digit",
              month: "short",
            })}
          </time>
        </li>
      ))}
    </ul>
  );
}

/**
 * Names a kind or a reason, falling back to the raw code.
 *
 * A kind added to the schema before the copy catches up should read as an
 * unfamiliar word, not crash the screen somebody opened to find out why they
 * heard nothing.
 */
function label(
  translate: ReturnType<typeof useTranslations<"account.support">>,
  group: string,
  value: string,
): string {
  const key = `${group}.${value}`;
  // biome-ignore lint/suspicious/noExplicitAny: the key is a runtime code, not a literal in the catalogue.
  return translate.has(key as any) ? translate(key as any) : value;
}
