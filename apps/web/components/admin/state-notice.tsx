"use client";

import { EmptyState } from "@omniflow/ui/empty-state";
import { AlertTriangle, Inbox, Lock, SearchX } from "lucide-react";
import { useTranslations } from "next-intl";
import type { ReactNode } from "react";

import { useSession } from "@/lib/session";

type Variant = "danger" | "empty" | "filtered" | "forbidden";

const ICONS: Record<Variant, ReactNode> = {
  danger: <AlertTriangle />,
  empty: <Inbox />,
  filtered: <SearchX />,
  forbidden: <Lock />,
};

/**
 * The shared treatment for every non-happy state a panel page can be in.
 *
 * Keeping them in one component is what makes "empty because there is nothing"
 * and "empty because your filter matched nothing" visibly different, which is
 * the distinction operators otherwise mistake for a broken page.
 */
export function StateNotice({
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

/** The standard treatment for a route the operator's roles do not reach. */
export function PermissionDenied() {
  const translate = useTranslations("admin");
  return (
    <StateNotice
      description={translate("states.forbiddenDescription")}
      title={translate("states.forbidden")}
      variant="forbidden"
    />
  );
}

/**
 * Renders children only when the operator holds every listed permission.
 *
 * This is presentation, not enforcement: the API checks the same permissions on
 * every request, so a bypass of this component grants nothing.
 */
export function RequirePermission({
  children,
  permissions,
}: {
  children: ReactNode;
  permissions: string[];
}) {
  const { canAll } = useSession();
  return canAll(...permissions) ? children : <PermissionDenied />;
}
