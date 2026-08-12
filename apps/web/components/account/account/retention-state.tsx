"use client";

import { Badge } from "@omniflow/ui/badge";
import { useFormatter, useTranslations } from "next-intl";

import type { PrivacyOverview } from "@/components/account/account/types";

/**
 * The tone each account status is shown in. Anything but active is worth
 * noticing, and the keys double as the set the catalogue is known to cover — a
 * status this build has never heard of falls back to the neutral wording rather
 * than rendering a missing-message key at the customer.
 */
const STATUS_TONE: Record<string, "danger" | "success" | "warning"> = {
  active: "success",
  deleted: "danger",
  suspended: "warning",
};

/**
 * What state the account is in, and how long its records are kept.
 *
 * A customer looking at a privacy screen is asking two questions: what is
 * happening to my account, and when does this installation stop holding my
 * data. The dated instants answer the second one; a screen that showed only the
 * status would leave a person with no way to tell whether "deleted" meant gone.
 */
export function RetentionState({ retention }: { retention: PrivacyOverview["retention"] }) {
  const translate = useTranslations("account.account");
  const format = useFormatter();

  function moment(value: string | null): string | null {
    if (!value) {
      return null;
    }
    return format.dateTime(new Date(value), { day: "numeric", month: "long", year: "numeric" });
  }

  const status = retention.status in STATUS_TONE ? retention.status : "unknown";
  const rows: { key: string; value: string }[] = [];
  for (const field of ["suspendedAt", "deletedAt", "anonymizedAt", "retentionUntil"] as const) {
    const value = moment(retention[field]);
    if (value) {
      rows.push({ key: field, value });
    }
  }

  return (
    <section className="space-y-3 rounded-xl border border-border bg-card p-4">
      <div className="flex items-center justify-between gap-3">
        <p className="min-w-0 font-mono text-[11px] text-subtle-foreground">
          {translate("retention.status")}
        </p>
        <Badge variant={STATUS_TONE[status] ?? "neutral"}>
          {translate(`retention.state.${status}`)}
        </Badge>
      </div>

      <p className="text-[12.5px] text-muted-foreground leading-relaxed">
        {translate(`retention.description.${status}`)}
      </p>

      {rows.length > 0 && (
        <dl className="space-y-1.5 border-border border-t pt-3">
          {rows.map((row) => (
            <div className="flex items-baseline justify-between gap-3" key={row.key}>
              <dt className="text-[12.5px] text-muted-foreground">
                {translate(`retention.${row.key}`)}
              </dt>
              <dd className="shrink-0 font-mono text-[11px] text-foreground">{row.value}</dd>
            </div>
          ))}
        </dl>
      )}
    </section>
  );
}
