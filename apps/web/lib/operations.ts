"use client";

import { useCallback, useState } from "react";

import { ApiError, apiFetch } from "./api";

/**
 * Shapes returned by the /v1/panel operations endpoints.
 *
 * They mirror the Go structs in `internal/panelpg`, which are the same values
 * `api/openapi.yaml` describes. They are declared here rather than imported
 * from the generated client for the reason recorded in `lib/api.ts`: the panel
 * uses the shared typed transport, not the generated SWR hooks, because those
 * carry neither the session cookie nor the CSRF token.
 */

export type Metric = {
  key: string;
  definition: string;
  value: number;
  /** The same measure over the preceding window; absent for a point-in-time total. */
  comparison?: number;
};

export type DependencyCheck = {
  name: string;
  healthy: boolean;
  error?: string;
  checkedAt: string;
  latencyMs: number;
  consecutiveFailures?: number;
};

export type ProviderHealth = {
  provider: string;
  merchantId?: string;
  enabled: boolean;
  connectionStatus: string;
  webhookStatus: string;
  connectionCheckedAt?: string;
  webhookLastEventAt?: string;
};

export type HealthReport = {
  healthy: boolean;
  dependencies: DependencyCheck[] | null;
  paymentProviders: ProviderHealth[] | null;
  goodsProviders: { slug: string; status: string; enabled: boolean; lowBalance: boolean }[] | null;
};

export type MaintenanceState = {
  active: boolean;
  source: string;
  reason: string;
  noticeRu: string;
  noticeEn: string;
  expectedReturnAt?: string;
  activatedAt?: string;
  updatedAt: string;
};

export type Incident = {
  id: string;
  action: string;
  source: string;
  reason: string;
  actorType: string;
  occurredAt: string;
};

export type RevenueLine = {
  currency: string;
  paidMinor: number;
  walletMinor: number;
  refundedMinor: number;
  orderCount: number;
  previousPaidMinor?: number;
};

export type AttentionItem = {
  key: string;
  severity: "alert" | "warning";
  count: number;
  href: string;
};

export type Dashboard = {
  windowSeconds: number;
  generatedAt: string;
  timezone: string;
  customers: Metric[];
  subscriptions: Metric[];
  payments: Metric[];
  revenue: RevenueLine[] | null;
  support: Metric[];
  operations: Metric[];
  attention: AttentionItem[] | null;
};

export type CustomerSummary = {
  id: string;
  status: string;
  locale: string;
  timezone: string;
  createdAt: string;
  telegramId?: number;
  suspendedAt?: string;
  deletedAt?: string;
};

export type CustomerProfile = CustomerSummary & {
  activeSubscriptions: number;
  orderCount: number;
  openTickets: number;
  referralCount: number;
  openFlags: number;
  allowlisted: boolean;
};

export type SubscriptionDetail = {
  id: string;
  slot: number;
  label: string;
  status: string;
  remnawaveUserId?: number;
  remnawaveUsername?: string;
  entitlementId?: string;
  entitlementStatus?: string;
  startsAt?: string;
  endsAt?: string;
  trafficAllowanceBytes?: number;
  deviceLimit?: number;
  planCode?: string;
  planVersion?: number;
};

export type OrderSummary = {
  id: string;
  state: string;
  operation: string;
  currency: string;
  subtotalMinor: number;
  discountMinor: number;
  walletMinor: number;
  externalMinor: number;
  paidMinor: number;
  refundedMinor: number;
  customerId: string;
  subscriptionId?: string;
  createdAt: string;
  updatedAt: string;
};

export type LedgerLine = {
  id: string;
  transactionId: string;
  type: string;
  referenceType: string;
  referenceId: string;
  reason?: string;
  currency: string;
  amountMinor: number;
  createdAt: string;
};

export type PlanSummary = {
  id: string;
  code: string;
  kind: string;
  visible: boolean;
  sortOrder: number;
  maxConcurrentPerCustomer?: number;
  latestVersion: number;
  orderLineCount: number;
  archivedAt?: string;
};

export type Promotion = {
  id: string;
  code: string;
  kind: string;
  value: number;
  currency?: string;
  active: boolean;
  stackable: boolean;
  precedence: number;
  perCustomerLimit: number;
  redemptionLimit?: number;
  redemptionCount: number;
  discountMinor: number;
  startsAt?: string;
  endsAt?: string;
};

/**
 * A promotion pointed at one customer.
 *
 * The discount itself lives on the promotion, so the stacking rules,
 * precedence, and rejection reasons that already govern promotions govern this
 * too. What the offer adds is a target, a validity window, its own copy in both
 * languages, and single-use redemption.
 */
/**
 * One immutable version of a plan.
 *
 * A version is never edited: once an order references it, changing it would
 * re-price history. The editor publishes the next version instead.
 */
export type PlanVersion = {
  id: string;
  version: number;
  billingPeriod: string;
  durationSeconds: number;
  trafficAllowanceBytes?: number;
  deviceLimit?: number;
  squadIds: string[];
  squadSelection: string;
  minSelectableSquads: number;
  maxSelectableSquads?: number;
  upgradePolicy: string;
  downgradePolicy: string;
  cancellationPolicy: string;
  gracePeriodSeconds: number;
  trialEligibility: string;
  recurringCapable: boolean;
  prices: Record<string, number>;
  createdAt: string;
  retiredAt?: string;
};

export type PersonalOffer = {
  id: string;
  customerId: string;
  promotionId: string;
  planId?: string;
  titleRu: string;
  titleEn: string;
  termsRu?: string;
  termsEn?: string;
  status: string;
  startsAt: string;
  expiresAt: string;
  orderId?: string;
  createdAt: string;
  resolvedAt?: string;
};

export type FulfillmentOperation = {
  id: string;
  entitlementId: string;
  customerId: string;
  operation: string;
  status: string;
  attemptCount: number;
  nextAttemptAt: string;
  lastErrorCode?: string;
  correlationId: string;
  createdAt: string;
};

export type WebhookEvent = {
  id: string;
  provider: string;
  providerEventId: string;
  signatureValid: boolean;
  status: string;
  errorCode?: string;
  receivedAt: string;
  processedAt?: string;
};

export type BlocklistMatch = {
  id: string;
  customerId: string;
  sourceSlug: string;
  sourceName: string;
  subjectKind: string;
  status: string;
  decisionReason?: string;
  detectedAt: string;
  decidedAt?: string;
};

export type AnomalySignal = {
  id: string;
  metric: string;
  severity: "warning" | "alert";
  subjectType: string;
  subjectId: string;
  observed: number;
  threshold: number;
  sampleSize: number;
  windowStart: string;
  windowEnd: string;
  evidence: Record<string, unknown>;
  status: string;
  detectedAt: string;
};

export type AnomalyRule = {
  metric: string;
  enabled: boolean;
  windowSeconds: number;
  warnThreshold: number;
  alertThreshold: number;
  minimumSample: number;
};

export type Gift = {
  id: string;
  orderId: string;
  senderId: string;
  recipientId?: string;
  kind: string;
  currency: string;
  creditMinor?: number;
  codeHint: string;
  status: string;
  claimAttempts: number;
  expiresAt: string;
  claimedAt?: string;
  createdAt: string;
};

export type GoodsProduct = {
  id: string;
  code: string;
  providerSlug: string;
  kind: string;
  durationMonths?: number;
  starQuantity?: number;
  visible: boolean;
  sortOrder: number;
  archivedAt?: string;
  localizations?: Record<string, { name: string; description: string }>;
  pricing?: {
    currency: string;
    markupBps: number;
    rounding: string;
    fixedAmountMinor?: number;
    quoteTtlSeconds: number;
  };
};

export type GoodsOrder = {
  orderId: string;
  customerId: string;
  productId: string;
  quantity: number;
  recipient: string;
  recipientIsSelf: boolean;
  quotedCostMinor: number;
  quotedPriceMinor: number;
  marginMinor: number;
  currency: string;
  status: string;
  deliveryStatus?: string;
  deliveryAttempts?: number;
  failureClass?: string;
  errorCode?: string;
  refunded: boolean;
  createdAt: string;
};

export type ProviderSettings = {
  provider: string;
  merchantId: string;
  enabled: boolean;
  displayOrder: number;
  credentialsSet: boolean;
  webhookSecretSet: boolean;
  adapterRecurring: boolean;
  recurringEnabled: boolean;
  recurringTestStatus: string;
  connectionStatus: string;
  connectionErrorCode?: string;
  webhookStatus: string;
  webhookLastEventAt?: string;
};

export type CommerceSettings = {
  topUp: {
    enabled: boolean;
    currency: string;
    presetsMinor: number[] | null;
    minimumMinor: number;
    maximumMinor: number;
    windowSeconds: number;
    windowLimitMinor: number;
  };
  subscriptions: { multiEnabled: boolean; maxPerCustomer: number };
  updatedAt: string;
};

/**
 * One contrast problem in the operator's palette.
 *
 * The ratio is computed by the API rather than in the browser, and there is
 * deliberately no second implementation of the WCAG formula here: two of them
 * would eventually disagree about which palettes are allowed, and only the one
 * that refuses saves would be right.
 */
export type BrandingWarning = {
  code: "pair_unreadable" | "pair_below_aa";
  mode: "light" | "dark";
  foreground: string;
  background: string;
  ratio: number;
  blocking: boolean;
};

export type Palette = Record<string, string>;

export type ThemeSettings = {
  theme: {
    light?: Palette;
    dark?: Palette;
    radius: string;
    density: string;
    allowedThemes: string[];
    defaultTheme: string;
  };
  css: string;
  warnings: BrandingWarning[];
  /** The tokens this build honours, so the screen never offers one it cannot set. */
  themable: string[];
  version: number;
  updatedAt: string;
  updatedBy?: string;
};

export type BrandingAsset = {
  kind: string;
  contentType: string;
  byteSize: number;
  checksum: string;
  updatedAt: string;
  updatedBy?: string;
};

export type BrandingAssets = {
  items: BrandingAsset[] | null;
  kinds: string[];
  contentTypes: string[];
  maxBytes: number;
};

/**
 * The connection guidance an installation gives its customers.
 *
 * The same rows are read by the Telegram bot and by the customer web panel, so
 * a change here changes both — which is the property this catalogue exists to
 * keep. The labels are stored per locale rather than resolved from a message
 * catalogue, because an operator who adds a platform has no way to add a key to
 * a compiled one.
 */
export type ConnectPlatform = {
  slug: string;
  labelEn: string;
  labelRu: string;
  enabled: boolean;
  sortOrder: number;
  updatedAt: string;
  updatedBy?: string;
};

export type ConnectClient = {
  id?: string;
  platform: string;
  name: string;
  /** Concatenated with the subscription link. Validated by the API, not here. */
  scheme: string;
  downloadUrl?: string;
  instructionsEn?: string;
  instructionsRu?: string;
  enabled: boolean;
  sortOrder: number;
  updatedAt: string;
  updatedBy?: string;
};

export type ConnectCatalogue = {
  platforms: ConnectPlatform[];
  clients: ConnectClient[];
};

/**
 * Sales over a period the operator chose.
 *
 * Provider money and wallet credit are separate fields on purpose and must not
 * be added: the balance was already counted as revenue when it was funded.
 * Refunds are keyed on the date they were issued rather than on the sale they
 * reverse, so re-running a report over a closed period returns the same figures.
 */
export type SalesLine = {
  operation: string;
  currency: string;
  orders: number;
  subtotalMinor: number;
  discountMinor: number;
  paidMinor: number;
  walletMinor: number;
};

export type PlanSales = {
  planCode: string;
  planVersion: number;
  billingPeriod: string;
  currency: string;
  orders: number;
  grossMinor: number;
};

export type DaySales = {
  /** `YYYY-MM-DD` in the timezone the report was asked for. */
  day: string;
  currency: string;
  orders: number;
  paidMinor: number;
  walletMinor: number;
};

export type PeriodRefunds = {
  currency: string;
  refunds: number;
  refundedMinor: number;
};

export type SalesReport = {
  since: string;
  until: string;
  timezone: string;
  currency?: string;
  byOperation: SalesLine[];
  byPlan: PlanSales[];
  byDay: DaySales[];
  refunds: PeriodRefunds[];
  /**
   * A cohort figure: the denominator is trials claimed in the period, the
   * numerator counts conversions at any later time. A period ending today reads
   * low by construction, which the screen says rather than hides.
   */
  trials: { trials: number; converted: number; cohort: boolean };
  generatedAt: string;
};

/**
 * Payment health per provider.
 *
 * Two rates, and the difference between them is the point. `settlementRate` is
 * settled ÷ (settled + failed): how often the adapter completes a payment it was
 * asked to take. `completionRate` adds the customers who walked away, which is
 * the funnel rather than the acquirer. Both are absent rather than zero when
 * nothing reached a decision, because "nobody paid" and "everybody failed" are
 * opposite facts.
 */
export type ProviderHealthLine = {
  provider: string;
  currency: string;
  intents: number;
  settled: number;
  failed: number;
  abandoned: number;
  /** In neither rate's denominator: an intent created minutes ago has not failed. */
  stillOpen: number;
  settledMinor: number;
  medianSettleSeconds: number;
  p95SettleSeconds: number;
  oldestOpenSeconds: number;
  settlementRate?: number;
  completionRate?: number;
};

export type ProviderHealthDay = {
  day: string;
  provider: string;
  intents: number;
  settled: number;
  failed: number;
};

export type WebhookHealthLine = {
  provider: string;
  received: number;
  rejected: number;
  failed: number;
  processed: number;
};

export type PaymentHealthReport = {
  since: string;
  until: string;
  timezone: string;
  providers: ProviderHealthLine[];
  byDay: ProviderHealthDay[];
  webhooks: WebhookHealthLine[];
  generatedAt: string;
};

/**
 * Traffic, read live from Remnawave on every request.
 *
 * Omniflow stores none of this. Remnawave owns traffic, nodes, and connections;
 * the report adds the one thing Omniflow can — which customer holds a given
 * Remnawave user — and keeps no history, because keeping one would be the first
 * step towards Omniflow having an opinion about traffic.
 */
export type NodeLine = {
  name: string;
  countryCode?: string;
  connected: boolean;
  disabled: boolean;
  usedBytes: number;
  limitBytes: number;
  /** Absent when the node has no limit: it cannot be "filling up". */
  usedShare?: number;
  usersOnline?: number;
};

export type ConsumerLine = {
  remnawaveId: number;
  username: string;
  usedBytes: number;
  lifetimeBytes: number;
  limitBytes: number;
  /** Empty for a Remnawave user Omniflow did not create. */
  customerId?: string;
  label?: string;
  status?: string;
};

export type TrafficReport = {
  nodes: NodeLine[];
  /** False when the panel did not answer. An empty list means neither thing. */
  nodesReported: boolean;
  nodesDetail?: string;
  consumers: ConsumerLine[];
  /** How much of the user base the ranking covers, and how much there is. */
  scanned: number;
  total: number;
};

/**
 * An information page: the FAQ, the terms, the offer, the privacy policy.
 *
 * `slug` is the identity rather than a field beside a generated one, because the
 * address is the thing that has to be stable — a payment provider approved a
 * URL. The body is the operator's source text; the public surfaces receive it
 * parsed into blocks and never as HTML.
 */
export type InfoPageLocale = {
  locale: string;
  title: string;
  body: string;
};

export type InfoPage = {
  slug: string;
  kind: string;
  /** Absent for a draft, which answers 404 publicly. */
  publishedAt?: string;
  /** A published page can be unlisted: a stable address without a menu entry. */
  listed: boolean;
  sortOrder: number;
  locales?: InfoPageLocale[];
  availableLocales?: string[];
  updatedAt: string;
  updatedBy?: string;
};

export type InfoPageList = {
  items: InfoPage[] | null;
  kinds: string[];
};

/**
 * A wholesale code batch.
 *
 * The plaintext codes appear in exactly one place — the response to creating a
 * batch — and nowhere else, because only their SHA-256 is stored. `GeneratedBatch`
 * is a separate type from `CodeBatch` for that reason: there is no field on the
 * stored shape to put a code in, so no listing screen can render one by accident.
 */
export type CodeBatch = {
  id: string;
  reference: string;
  planCode: string;
  planVersionId?: string;
  planVersion: number;
  billingPeriod: string;
  quantity: number;
  unitPriceMinor: number;
  currency: string;
  note?: string;
  /** Codes nobody has redeemed yet, and therefore what revoking would kill. */
  issued: number;
  redeemed: number;
  revoked: number;
  expiresAt?: string;
  revokedAt?: string;
  revokeReason?: string;
  createdAt: string;
  createdBy?: string;
};

export type GeneratedBatch = {
  batch: CodeBatch;
  /** Returned once. There is no way to ask for them again. */
  codes: string[];
};

export type CodeBatchList = {
  items: CodeBatch[] | null;
  maxBatchSize: number;
};

/**
 * Merging one customer account into another.
 *
 * The preview is the substance: a merge cannot be undone, so everything that
 * would move is counted and every reason it cannot happen is listed before
 * anything does. The API recomputes the blockers when the merge is applied, so a
 * preview that went stale between the screen and the button cannot let a refused
 * merge through.
 */
export type MergeBalance = { currency: string; balanceMinor: number };

export type MergeSide = {
  id: string;
  status: string;
  createdAt: string;
  activeSubscriptions: number;
  orders: number;
  tickets: number;
  identities: number;
  referralsMade: number;
  trialClaims: number;
  wallet: MergeBalance[];
  /** Set when this account has already been absorbed by another. */
  mergedInto?: string;
};

export type MergePreview = {
  source: MergeSide;
  target: MergeSide;
  /** Empty when the merge can proceed. */
  blockers: string[];
  /** Consequences worth knowing rather than reasons to stop. */
  notes: string[];
};

export type Page<Item> = { items: Item[] | null; nextCursor?: string };
export type Listing<Item> = { items: Item[] | null };

/**
 * Runs a panel mutation and reports the outcome.
 *
 * Every mutation carries the operator's reason in `X-Operator-Reason`. The API
 * refuses the changes that require one, so this hook is where a page collects
 * it rather than each form growing its own field and one of them forgetting.
 *
 * `pending` and `error` are returned rather than thrown, because a failed
 * mutation should leave the operator on the page they were on, looking at what
 * went wrong.
 */
export function useOperatorAction() {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const run = useCallback(
    async (
      path: string,
      options: { method: string; body?: unknown; reason?: string; idempotencyKey?: string } = {
        method: "POST",
      },
    ): Promise<boolean> => {
      setPending(true);
      setError(null);
      try {
        const headers: Record<string, string> = {};
        if (options.reason) {
          headers["X-Operator-Reason"] = options.reason;
        }
        if (options.idempotencyKey) {
          headers["Idempotency-Key"] = options.idempotencyKey;
        }
        await apiFetch(path, {
          method: options.method,
          headers,
          body: options.body === undefined ? undefined : JSON.stringify(options.body),
        });
        return true;
      } catch (caught) {
        setError(caught instanceof ApiError ? caught : new ApiError(0, null));
        return false;
      } finally {
        setPending(false);
      }
    },
    [],
  );

  return { run, pending, error, reset: () => setError(null) };
}

/**
 * Formats an integer minor-unit amount for display.
 *
 * The exponent is per currency because a shop that prices in Stars has no minor
 * unit at all, and rendering 100 Stars as "1.00" would be wrong rather than
 * merely ugly.
 */
export function formatMoney(
  minor: number,
  currency: string,
  locale: string,
  // `compact` is for a chart axis, where the full figure would be wider than
  // the plot it labels. It is never the right form for a number somebody has to
  // act on, which is why it has to be asked for.
  options: { compact?: boolean } = {},
): string {
  const exponent = ZERO_DECIMAL.has(currency) ? 0 : 2;
  const amount = minor / 10 ** exponent;
  const digits = options.compact
    ? { maximumFractionDigits: 1 }
    : {
        maximumFractionDigits: exponent,
        minimumFractionDigits: exponent,
      };
  try {
    return new Intl.NumberFormat(locale, {
      style: "currency",
      currency,
      notation: options.compact ? "compact" : "standard",
      ...digits,
    }).format(amount);
  } catch {
    // A currency Intl does not know — Telegram Stars, for one — still has to
    // render as something an operator can read.
    return `${amount.toFixed(options.compact ? 0 : exponent)} ${currency}`;
  }
}

const ZERO_DECIMAL = new Set(["XTR", "JPY", "KRW"]);

/** Formats a byte count at human scale. */
export function formatBytes(bytes: number): string {
  if (bytes <= 0) {
    return "0 B";
  }
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / 1024 ** index).toFixed(index === 0 ? 0 : 1)} ${units[index]}`;
}

/** Formats a duration in seconds as a coarse, readable span. */
export function formatDuration(seconds: number): string {
  if (seconds < 60) {
    return `${Math.round(seconds)}s`;
  }
  if (seconds < 3600) {
    return `${Math.round(seconds / 60)}m`;
  }
  if (seconds < 86_400) {
    return `${Math.round(seconds / 3600)}h`;
  }
  return `${Math.round(seconds / 86_400)}d`;
}
