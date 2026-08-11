"use client";

import { EmptyState } from "@omniflow/ui/empty-state";
import { Skeleton } from "@omniflow/ui/skeleton";
import { AlertTriangle, Inbox, Lock, WifiOff } from "lucide-react";
import { useFormatter, useTranslations } from "next-intl";
import type { ReactNode } from "react";

type Variant = "danger" | "empty" | "forbidden" | "offline";

const ICONS: Record<Variant, ReactNode> = {
  danger: <AlertTriangle />,
  empty: <Inbox />,
  forbidden: <Lock />,
  offline: <WifiOff />,
};

/**
 * The shared treatment for every non-happy state a customer page can be in.
 *
 * It is a customer-side twin of the panel's version rather than a shared
 * component: the panel's carries a permission-denied case that reads the
 * operator session, and a customer surface must not import that.
 */
export function AccountNotice({
  action,
  description,
  title,
  variant = "empty",
}: {
  action?: ReactNode;
  description?: ReactNode;
  title: ReactNode;
  variant?: Variant;
}) {
  return (
    <EmptyState action={action} description={description} icon={ICONS[variant]} title={title} />
  );
}

/**
 * The banner shown when Remnawave could not be reached.
 *
 * The page still renders what Omniflow itself knows — the plan, the period, the
 * expiry — and this says which figures are not current. Silently showing the
 * last observed traffic as though it were live is the failure mode worth
 * avoiding: a customer would make decisions on a number that stopped moving.
 */
export function StaleDataNotice() {
  const translate = useTranslations("account");
  return (
    <p
      className="rounded-lg border border-warning/40 bg-warning/10 px-4 py-3 text-[12.5px] text-foreground leading-relaxed"
      role="status"
    >
      {translate("states.stale")}
    </p>
  );
}

/**
 * The service-wide incident banner.
 *
 * It sits above everything because an incident explains the state of every
 * subscription below it. The operator's own wording is used when they wrote
 * one; the fallback copy is here so a maintenance window with no custom message
 * still says something useful rather than nothing.
 */
export function ServiceNotice({
  expectedReturnAt,
  message,
}: {
  expectedReturnAt?: string;
  message?: string;
}) {
  const translate = useTranslations("account");
  const format = useFormatter();
  return (
    <div
      className="space-y-1 rounded-lg border border-warning/40 bg-warning/10 px-4 py-3"
      role="status"
    >
      <p className="font-semibold text-[13.5px]">{translate("states.maintenance")}</p>
      <p className="text-[12.5px] leading-relaxed">
        {message?.trim() || translate("states.maintenanceDescription")}
      </p>
      {expectedReturnAt && (
        <p className="font-mono text-[11px] text-muted-foreground">
          {translate("states.maintenanceReturn", {
            time: format.dateTime(new Date(expectedReturnAt), {
              day: "numeric",
              hour: "2-digit",
              minute: "2-digit",
              month: "short",
            }),
          })}
        </p>
      )}
    </div>
  );
}

/** A list placeholder that keeps the page from reflowing when data lands. */
export function ListSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div aria-hidden className="space-y-3">
      {Array.from({ length: rows }, (_, index) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: placeholders have no identity
        <Skeleton className="h-24 w-full rounded-lg" key={index} />
      ))}
    </div>
  );
}

/** The heading style the design uses above every group. */
export function SectionLabel({ children }: { children: ReactNode }) {
  return (
    <h2 className="px-1 font-medium font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.14em]">
      {children}
    </h2>
  );
}
