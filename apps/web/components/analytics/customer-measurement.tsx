import { ScopedIntlProvider } from "@/components/intl/scoped-intl-provider";
import { readAnalytics } from "@/lib/analytics";
import { readNonce } from "@/lib/nonce";

import { Measurement } from "./measurement";

/**
 * The operator's advertising measurement, for the public and customer pages only.
 *
 * It is rendered from the customer-facing route groups rather than the root
 * layout, so no counter ever loads on an operator page: the panel shows customer
 * records, payment data, and access links, and an advertising provider has no
 * business observing any of it, whatever the operator configured.
 *
 * It carries its own copy, so the consent request's text reaches the browser
 * only on an installation that measures something.
 */
export async function CustomerMeasurement() {
  const [analytics, nonce] = await Promise.all([readAnalytics(), readNonce()]);
  if (!analytics.measurable) {
    return null;
  }
  return (
    <ScopedIntlProvider namespaces={["analytics"]}>
      {/* Renders nothing until the visitor has agreed to it. */}
      <Measurement analytics={analytics} nonce={nonce} />
    </ScopedIntlProvider>
  );
}
