import type { ReactNode } from "react";

import { ScopedIntlProvider } from "@/components/intl/scoped-intl-provider";

/**
 * Every customer route, signed in or not.
 *
 * Its only job is to hand the customer panel's copy to the browser here rather
 * than at the root, so the operator panel's catalogue never rides along with a
 * customer page.
 */
export default function AccountRootLayout({ children }: { children: ReactNode }) {
  return <ScopedIntlProvider namespaces={["common", "account"]}>{children}</ScopedIntlProvider>;
}
