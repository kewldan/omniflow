import type { ReactNode } from "react";

import { ScopedIntlProvider } from "@/components/intl/scoped-intl-provider";

/**
 * Every operator route, signed in or not.
 *
 * It hands the operator panel's copy to the browser here rather than at the
 * root, so the panel's catalogue — the larger half of the whole — is paid for
 * only by the people who use the panel.
 */
export default function AdminRootLayout({ children }: { children: ReactNode }) {
  return <ScopedIntlProvider namespaces={["common", "admin"]}>{children}</ScopedIntlProvider>;
}
