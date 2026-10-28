"use client";

import { StatusNotice } from "@omniflow/ui/empty-state";
import { useTranslations } from "next-intl";
import type { ReactNode } from "react";

import { useSession } from "@/lib/session";

/**
 * The shared treatment for every non-happy state a panel page can be in; the
 * component itself is the one both panels use, from `@omniflow/ui`.
 */
export { StatusNotice as StateNotice };

/** The standard treatment for a route the operator's roles do not reach. */
export function PermissionDenied() {
  const translate = useTranslations("admin");
  return (
    <StatusNotice
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
