import { apiFetch } from "@/lib/api";
import { clearAttribution, readAttribution, readMeasurementChoice } from "@/lib/attribution";

/*
 * Kept out of `lib/attribution.ts` deliberately.
 *
 * That module is reached from the root layout, which every route in both panels
 * shares. This one reaches the API client, and the API client is the whole
 * generated surface — pulling it into the root layout put a quarter of a
 * megabyte of operations and schemas into the first load of every page,
 * including the two sign-in screens that call none of them.
 *
 * Only the checkout imports this, which is the only place a purchase exists to
 * attach anything to.
 */

/**
 * Attaches what is being carried to an order that now exists, then forgets it.
 *
 * Called once, right after a purchase is confirmed. It never throws: a customer
 * who has just paid must reach their order whatever the operator's analytics is
 * doing, and an attribution that did not attach is one missing row in a report
 * rather than a failed purchase.
 *
 * It is cleared afterwards either way. The click has been spent — carrying it
 * into the next purchase would credit the same advertisement twice.
 */
export async function attachAttribution(orderId: string): Promise<void> {
  if (readMeasurementChoice() !== "granted") {
    return;
  }
  const attribution = readAttribution();
  if (!attribution) {
    return;
  }
  try {
    await apiFetch(`/v1/account/orders/${orderId}/attribution`, {
      body: JSON.stringify(attribution),
      method: "POST",
    });
  } catch {
    // Deliberately silent. Nothing a customer can act on happened.
  } finally {
    clearAttribution();
  }
}
