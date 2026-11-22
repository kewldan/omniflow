import "server-only";

import { headers } from "next/headers";

/**
 * Whether this request renders a customer surface — the cabinet, its sign-in,
 * the landing page, and the public documents — rather than the operator panel.
 *
 * The root layout puts `customer-ui` on <html> for those, so the customer
 * look applies from the first paint instead of after hydration. The proxy
 * sets the header from the same path rule the content security policy uses.
 * `SurfaceClass` keeps the class right when a client-side navigation crosses
 * from one panel to the other, which the root layout does not re-render for.
 */
export async function isCustomerSurface(): Promise<boolean> {
  const surface = (await headers()).get("x-surface");
  return surface !== null && surface !== "admin";
}
