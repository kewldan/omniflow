import type { ReactNode } from "react";

import { MiniAppProvider } from "@/components/account/miniapp";
import { CustomerMeasurement } from "@/components/analytics/customer-measurement";

/**
 * The landing page and the customer panel.
 *
 * This is where the operator's measurement is mounted — here and on the public
 * documents, never at the root, so it cannot reach the operator panel. The
 * Mini App adaptation is mounted here too, so the sign-in screen a Mini App
 * opens on already asks Telegram for full screen.
 */
export default function CustomerGroupLayout({ children }: { children: ReactNode }) {
  return (
    <MiniAppProvider>
      {children}
      <CustomerMeasurement />
    </MiniAppProvider>
  );
}
