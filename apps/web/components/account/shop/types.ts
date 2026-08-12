/**
 * The shop's wire types.
 *
 * They mirror `shopProductPayload` and `shopOrderPayload` in
 * `internal/httpapi/accountshop.go` field for field, including which fields are
 * optional: a missing `priceMinor` is the API saying it has no published price,
 * not a zero, and typing it as optional is what stops a screen from quietly
 * rendering "free".
 *
 * `kind`, `delivery.state`, and `failureReason` are typed as plain strings on
 * purpose. Their vocabularies are closed today, but a server one version ahead
 * can send a value this build has never heard of, and a union would make that a
 * type lie rather than a case to handle. The lookups below narrow them instead,
 * so an unknown value lands on a message that is honest about not knowing.
 */

/** One catalogue entry. */
export type ShopProduct = {
  id: string;
  code: string;
  kind: string;
  name: string;
  description: string;
  currency: string;
  /** The product's gateway is configured and sells this kind of goods. */
  available: boolean;
  /** False when no price is published yet — the detail screen quotes one. */
  priceKnown: boolean;
  priceMinor?: number;
  durationMonths?: number;
  starQuantity?: number;
};

/**
 * A price and the moment it stops applying.
 *
 * The two travel together everywhere, because a price without its expiry is a
 * number the panel cannot honour and the server will refuse.
 */
export type ShopQuote = {
  priceMinor: number;
  currency: string;
  expiresAt: string;
};

/** A promotion priced against the quote, without being redeemed. */
export type ShopPromo = {
  code: string;
  discountMinor: number;
  /** Set when the code was refused; the discount is then zero. */
  rejection?: string;
};

/** One product together with the quote issued when it was opened. */
export type ShopProductDetail = ShopProduct & {
  quantity: number;
  quote: ShopQuote;
  promo?: ShopPromo;
};

/** The normalised handle the review step returns. */
export type ShopRecipient = {
  recipient: string;
  /** The gateway confirmed the handle. False is the ordinary case today. */
  checked: boolean;
};

/** One purchase, as the customer's own history shows it. */
export type ShopOrder = {
  id: string;
  productName: string;
  kind: string;
  quantity: number;
  recipient: string;
  forSelf: boolean;
  currency: string;
  durationMonths?: number;
  starQuantity?: number;
  amounts: {
    priceMinor: number;
    discountMinor: number;
    walletMinor: number;
    externalMinor: number;
    paidMinor: number;
  };
  payment: {
    state: string;
    /** Money is still owed through the checkout surface. */
    required: boolean;
    /** False when money is owed and no configured provider can settle it. */
    possible: boolean;
  };
  delivery: {
    state: string;
    failureReason?: string;
    attempts: number;
    /** The state cannot be resolved by the customer; support is the only route. */
    supportHandoff: boolean;
    supportReference: string;
    refund?: { amountMinor: number; currency: string };
    deliveredAt?: string;
    updatedAt?: string;
  };
  createdAt: string;
  updatedAt: string;
};

/** One page of history, with the cursor for the next one. */
export type ShopOrderPage = {
  items: ShopOrder[];
  nextCursor?: string;
  nextCursorId?: string;
};

/** The ceiling `internal/accountshop` enforces on one order. */
export const MAX_QUANTITY = 10;

const KINDS = new Set(["telegram_premium", "telegram_stars"]);

const DELIVERY_STATES = new Set([
  "awaiting_payment",
  "cancelled",
  "delayed",
  "delivered",
  "failed",
  "needs_review",
  "polling",
  "queued",
  "refunded",
  "submitted",
]);

const FAILURE_REASONS = new Set([
  "ambiguous",
  "permanent",
  "provider_balance",
  "provider_unavailable",
  "recipient_invalid",
  "retryable",
]);

/** The delivery states that are still moving, so the screen keeps watching. */
const IN_FLIGHT = new Set(["awaiting_payment", "delayed", "polling", "queued", "submitted"]);

/** Narrows a product kind to one this build has copy for. */
export function kindKey(kind: string): string {
  return KINDS.has(kind) ? kind : "other";
}

/** Narrows a delivery state to one this build has copy for. */
export function deliveryKey(state: string): string {
  return DELIVERY_STATES.has(state) ? state : "unknown";
}

/** Narrows a failure classification to one this build has copy for. */
export function failureKey(reason: string | undefined): string {
  return reason && FAILURE_REASONS.has(reason) ? reason : "unknown";
}

/**
 * Whether an order is still expected to change on its own.
 *
 * The delivery screen polls while this holds and stops when it does not, so a
 * finished order costs nothing to leave open and an in-flight one updates
 * without the customer reloading.
 */
export function isDeliveryInFlight(state: string): boolean {
  return IN_FLIGHT.has(state);
}

/**
 * What is actually owed once a promotion has been taken off.
 *
 * It floors at zero rather than trusting the arithmetic: a discount larger than
 * the price is a state the server refuses, and a screen that rendered a
 * negative total would be inventing a refund.
 */
export function payableMinor(priceMinor: number, discountMinor: number): number {
  return Math.max(0, priceMinor - discountMinor);
}
