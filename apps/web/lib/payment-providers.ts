import { PaymentProvider } from "@omniflow/api-client/generated";

/**
 * Every payment adapter the API can report, taken from the generated enum so a
 * provider added to the contract cannot be missing from the screens that name
 * it. Anything outside this list renders as "another method" rather than as a
 * raw slug.
 */
export const PAYMENT_PROVIDERS: readonly string[] = Object.values(PaymentProvider);

/** The message key suffix for a provider, or `unknown` for one this build has no copy for. */
export function providerKey(provider: string): string {
  return PAYMENT_PROVIDERS.includes(provider) ? provider : "unknown";
}
