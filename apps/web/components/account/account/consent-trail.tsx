"use client";

import { Badge } from "@omniflow/ui/badge";
import { useFormatter, useTranslations } from "next-intl";

import type { PrivacyOverview } from "@/components/account/account/types";
import { AccountNotice } from "@/components/account/state";

/** The purposes the schema admits. Anything else is shown by its own name. */
const PURPOSES = ["terms", "privacy", "marketing", "profiling"] as const;

/**
 * What the customer has agreed to, and when each decision was made.
 *
 * The current position and the trail behind it are both shown because they
 * answer different questions. "Am I signed up for marketing?" is the first;
 * "when did I agree to that, and under which terms?" is the second, and an
 * installation that can only answer the first cannot show a customer why their
 * old choice still stands after the terms changed.
 *
 * This screen is deliberately read-only. Marketing consent is carried by the
 * contact channels above it — turning the flag off on every channel is what
 * withdraws it — and a second control that wrote consent directly would let the
 * two disagree about what the customer actually chose.
 */
export function ConsentTrail({ consents }: { consents: PrivacyOverview["consents"] }) {
  const translate = useTranslations("account.account");
  const format = useFormatter();

  /** A purpose the catalogue knows, or the raw name when a new one appears. */
  function purposeLabel(purpose: string): string {
    return PURPOSES.includes(purpose as (typeof PURPOSES)[number])
      ? translate(`consents.purpose.${purpose}`)
      : purpose;
  }

  function purposeHint(purpose: string): string {
    return PURPOSES.includes(purpose as (typeof PURPOSES)[number])
      ? translate(`consents.purposeHint.${purpose}`)
      : translate("consents.purposeHintUnknown");
  }

  const decided = PURPOSES.filter((purpose) => purpose in consents.current);
  const extra = Object.keys(consents.current).filter(
    (purpose) => !PURPOSES.includes(purpose as (typeof PURPOSES)[number]),
  );
  const current = [...decided, ...extra];

  return (
    <div className="space-y-3">
      {current.length === 0 ? (
        <AccountNotice
          description={translate("consents.emptyDescription")}
          title={translate("consents.empty")}
        />
      ) : (
        <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-card">
          {current.map((purpose) => (
            <li className="flex items-center justify-between gap-3 px-4 py-3.5" key={purpose}>
              <div className="min-w-0">
                <p className="font-medium text-[14px]">{purposeLabel(purpose)}</p>
                <p className="mt-0.5 text-[12px] text-subtle-foreground leading-relaxed">
                  {purposeHint(purpose)}
                </p>
              </div>
              <Badge variant={consents.current[purpose] ? "success" : "neutral"}>
                {translate(consents.current[purpose] ? "consents.granted" : "consents.withdrawn")}
              </Badge>
            </li>
          ))}
        </ul>
      )}

      {consents.history.length > 0 && (
        <details className="overflow-hidden rounded-xl border border-border bg-card">
          <summary className="cursor-pointer px-4 py-3.5 font-medium text-[13.5px] focus-visible:outline-2 focus-visible:outline-ring focus-visible:-outline-offset-2">
            {translate("consents.history", { count: consents.history.length })}
          </summary>
          <ul className="divide-y divide-border border-border border-t">
            {consents.history.map((record) => (
              <li
                className="flex items-start justify-between gap-3 px-4 py-3"
                key={`${record.purpose}-${record.occurredAt}-${String(record.granted)}`}
              >
                <div className="min-w-0">
                  <p className="text-[13px]">
                    {translate(record.granted ? "consents.grantedOn" : "consents.withdrawnOn", {
                      purpose: purposeLabel(record.purpose),
                    })}
                  </p>
                  <p className="mt-0.5 font-mono text-[10.5px] text-subtle-foreground">
                    {translate("consents.provenance", {
                      source: record.source,
                      version: record.policyVersion,
                    })}
                  </p>
                </div>
                <span className="shrink-0 font-mono text-[11px] text-subtle-foreground">
                  {format.dateTime(new Date(record.occurredAt), {
                    day: "numeric",
                    month: "short",
                    year: "numeric",
                  })}
                </span>
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}
