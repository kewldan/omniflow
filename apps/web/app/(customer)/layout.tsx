import type { ReactNode } from "react";

import { CustomerMeasurement } from "@/components/analytics/customer-measurement";

/**
 * The landing page and the customer panel.
 *
 * This is where the operator's measurement is mounted — here and on the public
 * documents, never at the root, so it cannot reach the operator panel.
 */
export default function CustomerGroupLayout({ children }: { children: ReactNode }) {
  return (
    <>
      {children}
      <CustomerMeasurement />
    </>
  );
}
