import type { Metadata } from "next";
import type { ReactNode } from "react";

import { AccountShell } from "@/components/account/account-shell";
import { AccountProvider } from "@/lib/account-session";

export const metadata: Metadata = {
  title: { default: "Account", template: "%s · Omniflow" },
  // A customer's own pages must never appear in a search index, however the
  // installation is deployed.
  robots: { index: false, follow: false },
};

/**
 * Authenticated customer routes.
 *
 * The sign-in screens deliberately sit outside this layout: they cannot depend
 * on a session, and rendering the shell around them would flash navigation the
 * visitor is not yet entitled to see.
 */
export default function AccountLayout({ children }: { children: ReactNode }) {
  return (
    <AccountProvider>
      <AccountShell>{children}</AccountShell>
    </AccountProvider>
  );
}
