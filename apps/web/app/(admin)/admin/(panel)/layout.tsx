import type { Metadata } from "next";
import type { ReactNode } from "react";

import { AdminShell } from "@/components/admin/admin-shell";
import { SessionProvider } from "@/lib/session";

export const metadata: Metadata = {
  title: { default: "Admin", template: "%s · Omniflow admin" },
  // The panel must never appear in a search index, however it is deployed.
  robots: { index: false, follow: false },
};

/**
 * Authenticated panel routes.
 *
 * The sign-in, two-factor, and setup screens deliberately sit outside this
 * layout: they cannot depend on a session, and rendering the shell around them
 * would flash navigation the visitor is not yet entitled to see.
 */
export default function AdminLayout({ children }: { children: ReactNode }) {
  return (
    <SessionProvider>
      <AdminShell>{children}</AdminShell>
    </SessionProvider>
  );
}
