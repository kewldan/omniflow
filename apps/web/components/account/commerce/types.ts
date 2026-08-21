/**
 * The commerce surface's wire types.
 *
 * They mirror `planPayload`, `checkoutPayload`, `orderPayload`, `paymentPayload`,
 * and `readWallet` in `internal/httpapi/accountcheckout.go` field for field,
 * including which fields are optional and which are nullable. The distinction
 * matters more here than anywhere else in the panel: `trafficAllowanceBytes` is
 * `null` for an unlimited plan and a number for a metered one, so typing it as a
 * plain number would let a screen advertise "0 GB" for the most generous plan in
 * the catalogue.
 *
 * The vocabularies — `operation`, `state`, `phase`, `handoff`, `status`, and
 * every rejection reason — are typed as plain strings on purpose. They are closed
 * sets today, but a server one version ahead can send a value this build has
 * never seen, and a union would make that a type lie rather than a case the copy
 * has to handle. The lookups in `reasons.tsx` narrow them instead, so an unknown
 * value lands on a message that is honest about not knowing.
 */

/** An integer minor-unit amount with the currency it is denominated in. */
export type Money = {
  amountMinor: number;
  currency: string;
};

/** One purchasable plan as the comparison screen shows it. */
export type PlanOffer = {
  planId: string;
  planVersionId: string;
  code: string;
  kind: string;
  name: string;
  description: string;
  sortOrder: number;
  billingPeriod: string;
  durationSeconds: number;
  gracePeriodSeconds: number;
  price: Money;
  recurringCapable: boolean;
  configurableSquads: boolean;
  /** The lifecycle actions this customer may start right now, server-decided. */
  operations: string[];
  eligible: boolean;
  /** The customer already holds this plan, so the card can say so. */
  held?: boolean;
  /** The stable machine reason the plan is refused, present only when it is. */
  ineligibleReason?: string;
  /** `null` means unlimited, which is not the same as a limit of zero. */
  trafficAllowanceBytes: number | null;
  deviceLimit: number | null;
};

/** One squad a customer may add to a plan. */
export type SelectableSquad = {
  squadId: string;
  label: string;
};

/** The plan version's squad selection policy together with what it offers. */
export type SquadOffer = {
  /** "automatic", "optional", or "required". */
  selection: string;
  minimum: number;
  /** `null` means every offered squad may be selected. */
  maximum: number | null;
  /** False when the customer has no choice to make and no picker should appear. */
  configurable: boolean;
  offered: SelectableSquad[];
};

/** One add-on that can be bought together with a plan. */
export type AddonOffer = {
  addonId: string;
  addonVersionId: string;
  code: string;
  kind: string;
  name: string;
  description: string;
  maxQuantity: number;
  proration: string;
  squadCount: number;
  price: Money;
  trafficBytes: number | null;
  deviceSlots: number | null;
};

/**
 * A campaign running against a plan.
 *
 * It names what the campaign takes off and never the code that redeems it: a
 * code is a bearer value the operator hands to a chosen audience, and printing
 * every one of them on the plan page would hand all of them to everybody. The
 * `code` field here is the campaign's own identifier, which the API only
 * publishes for campaigns this customer is already inside.
 */
export type PromotionOffer = {
  code: string;
  kind: string;
  value: number;
  currency?: string;
  startsAt?: string;
  endsAt?: string;
  eligible: boolean;
};

/** `GET /v1/account/plans` */
export type PlanCatalogue = {
  items: PlanOffer[];
  currency: string;
};

/** `GET /v1/account/plans/{planVersionID}` */
export type PlanDetail = PlanOffer & {
  squads: SquadOffer;
  addons: AddonOffer[];
  promotions: PromotionOffer[];
  termsUrl: string;
};

/**
 * One offered payment method with the currency and price it would charge.
 *
 * `amountMinor` is absent on the wallet's provider list: a top-up is not tied to
 * a plan, so there is no price to quote beside the method.
 */
export type PaymentChoice = {
  provider: string;
  currency: string;
  amountMinor?: number;
  recurring: boolean;
};

/**
 * The priced checkout, exactly as the server computed it.
 *
 * Every figure below is rendered as it arrives. None of them is derived in
 * React: the subtotal, the add-on total, the discount, what the wallet covers,
 * and what is still owed externally are one another's consequences, and a panel
 * that recomputed any of them would be a second implementation of the pricing
 * rules that would eventually disagree with the order the customer is charged
 * for.
 */
export type CheckoutQuote = {
  currency: string;
  subtotalMinor: number;
  addonMinor: number;
  discountMinor: number;
  walletBalanceMinor: number;
  walletAppliedMinor: number;
  externalMinor: number;
};

/** One subscription a lifecycle flow can act on. */
export type SubscriptionTarget = {
  id: string;
  slot: number;
  label: string;
  plan: string;
  status: string;
  endsAt?: string;
};

/**
 * Whether a target still carries an entitlement with time left.
 *
 * Mirrors `accountcheckout.TargetLive`: a purchase schedules from now and
 * supersedes what is there, so it is offered only for an empty slot or a new
 * subscription, never for one the customer is still paying for — the server
 * refuses that with `operation_forbidden`, and the pickers do not offer it.
 */
export function targetLive(target: SubscriptionTarget, now = Date.now()): boolean {
  if (["", "expired", "superseded", "failed"].includes(target.status)) {
    return false;
  }
  if (!target.endsAt) {
    return true;
  }
  return new Date(target.endsAt).getTime() > now;
}

/** The dashboard's phases under which a subscription holds no live entitlement. */
export const UNHELD_PHASES = ["none", "expired", "failed"];

/** One add-on attached to the open checkout. */
export type CheckoutAddon = {
  addonVersionId: string;
  quantity: number;
};

/** `GET /v1/account/checkout` — 404 when nothing is open, which is normal. */
export type CheckoutView = {
  id: string;
  planVersionId: string;
  plan: PlanOffer;
  operation: string;
  currency: string;
  provider: string;
  providers: PaymentChoice[];
  applyWallet: boolean;
  quote: CheckoutQuote;
  promoCode: string;
  /** The stable reason a code was refused. A refusal is a 200, not an error. */
  promoRejection?: string;
  subscriptionId: string;
  newSubscription: boolean;
  subscriptions: SubscriptionTarget[];
  /** The server needs a named subscription before this checkout can proceed. */
  targetRequired: boolean;
  /** False when the installation runs one subscription and no picker belongs here. */
  multiSubscription: boolean;
  squads: SquadOffer;
  selectedSquadIds: string[];
  /**
   * The server choice is not finished, so there is no price yet. The reason is
   * the commerce vocabulary the problem copy already explains —
   * `squad_selection_required`, `squad_selection_too_few`.
   */
  squadSelection: { required: boolean; reason?: string };
  /** False while the quote is withheld; the breakdown must not render zeros as a price. */
  quoteAvailable: boolean;
  addons: AddonOffer[];
  selectedAddons: CheckoutAddon[];
  termsUrl: string;
  expiresAt?: string;
};

/** The latest payment intent recorded against an order. */
export type OrderPayment = {
  id: string;
  provider: string;
  status: string;
  /** "hosted", "telegram_invoice", "manual", or "none". */
  handoff: string;
  checkoutUrl?: string;
  receiptUrl?: string;
};

/**
 * Provisioning progress, read from the fulfillment operation on the server.
 *
 * This is the reason the order screen survives a reload: the progress lives in
 * the database beside the entitlement, so a refresh, a second tab, or a switch
 * from the browser to the chat all show the same state. Nothing about it is
 * carried in React.
 */
export type OrderFulfillment = {
  status: string;
  attempts: number;
  /** The worker's own bounded code, never a provider message. */
  errorCode?: string;
  updatedAt?: string;
};

/** One refund recorded against an order's payments. */
export type OrderRefund = {
  status: string;
  amountMinor: number;
  currency: string;
  createdAt: string;
};

/** `GET /v1/account/orders/{orderID}` and one row of the history. */
export type OrderSummary = {
  id: string;
  state: string;
  operation: string;
  /** The customer-visible fold of order state, payment status, and fulfillment. */
  phase: string;
  currency: string;
  subtotalMinor: number;
  discountMinor: number;
  walletMinor: number;
  externalMinor: number;
  paidMinor: number;
  refundedMinor: number;
  plan: string;
  createdAt: string;
  subscriptionId: string;
  expiresAt?: string;
  payment?: OrderPayment;
  fulfillment?: OrderFulfillment;
  /** Present on the detail response only; the list omits it. */
  refunds?: OrderRefund[];
  /**
   * The methods that can settle this order, in its currency. Present on the
   * detail response while the order is pending and owes something, so the page
   * can offer a method without depending on the URL the checkout redirected
   * to.
   */
  paymentChoices?: PaymentChoice[];
  /** The method the checkout recorded, when that checkout is still attached. */
  preferredProvider?: string;
};

/** `GET /v1/account/orders` */
export type OrderPage = {
  items: OrderSummary[];
  nextCursor?: string;
  nextCursorId?: string;
};

/** `POST /v1/account/orders/{orderID}/payment` */
export type PaymentHandle = {
  id: string;
  provider: string;
  status: string;
  amountMinor: number;
  currency: string;
  handoff: string;
  checkoutUrl?: string;
};

/**
 * The customer's credit in one currency.
 *
 * Three figures rather than one: the total is what the ledger holds, the
 * reserved part is what unpaid orders have already claimed, and the available
 * part is what a new order may actually spend. A customer shown only the total,
 * who then watches a smaller amount apply at checkout, has been told the wrong
 * number twice.
 */
export type WalletBalance = {
  currency: string;
  totalMinor: number;
  reservedMinor: number;
  availableMinor: number;
};

/** One customer-visible ledger movement. */
export type WalletEntry = {
  id: string;
  type: string;
  amountMinor: number;
  currency: string;
  occurredAt: string;
  /** The operator's note on a correction, when they wrote one. */
  reason?: string;
};

/** The operator's top-up limits, as the wallet screen must obey them. */
export type TopUpPolicy = {
  enabled: boolean;
  minimumMinor: number;
  maximumMinor: number;
  /** Already filtered by the server against what this customer has credited. */
  presets: number[];
  /** What may still be credited inside the rolling window. */
  remainingWindowMinor: number;
  providers: PaymentChoice[];
};

/** `GET /v1/account/wallet` — balances, policy, and one page of the ledger. */
export type WalletView = {
  balances: WalletBalance[];
  currency: string;
  topUp: TopUpPolicy;
  entries: WalletEntry[];
  nextCursor?: string;
  nextCursorId?: string;
};

/** `POST /v1/account/wallet/top-up` */
export type TopUpResult = {
  orderId: string;
  currency: string;
  amountMinor: number;
  state: string;
  payment: PaymentHandle;
};
