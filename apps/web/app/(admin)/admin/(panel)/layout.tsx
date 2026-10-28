import type { Metadata } from "next";
import type { ReactNode } from "react";

import { AdminShell } from "@/components/admin/admin-shell";
import { OperatorTimeZone } from "@/components/intl/panel-time-zones";
import { areaTitle } from "@/lib/metadata";
import { SessionProvider } from "@/lib/session";

export async function generateMetadata(): Promise<Metadata> {
  return {
    // The installation's own name, never the product's: see areaTitle.
    title: await areaTitle("admin", true),
    // The panel must never appear in a search index, however it is deployed.
    robots: { index: false, follow: false },
  };
}

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
      <OperatorTimeZone>
        <AdminShell>{children}</AdminShell>
      </OperatorTimeZone>
    </SessionProvider>
  );
}
