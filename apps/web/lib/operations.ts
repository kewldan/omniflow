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
export function formatMoney(minor: number, currency: string, locale: string): string {
  const exponent = ZERO_DECIMAL.has(currency) ? 0 : 2;
  const amount = minor / 10 ** exponent;
  try {
    return new Intl.NumberFormat(locale, {
      style: "currency",
      currency,
      minimumFractionDigits: exponent,
      maximumFractionDigits: exponent,
    }).format(amount);
  } catch {
    // A currency Intl does not know — Telegram Stars, for one — still has to
    // render as something an operator can read.
    return `${amount.toFixed(exponent)} ${currency}`;
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
