/**
 * The shapes the marketing API returns.
 *
 * A segment carries its `explain` lines rather than its SQL. They are generated
 * from the filters themselves, so they cannot describe something the query does
 * not do — which is what makes them safe to show an operator as the definition
 * rather than as a comment about it.
 */

export type AudienceSegment = {
  id: string;
  code: string;
  nameEn: string;
  nameRu: string;
  filters: Record<string, unknown>;
  explain: string[] | null;
  /** How many customers match right now. It moves between reviews. */
  size: number;
};

export type MessageTemplate = {
  id: string;
  code: string;
  channel: string;
  kind: string;
  variables: string[] | null;
};

export type Campaign = {
  id: string;
  name: string;
  templateCode: string;
  segmentCode: string;
  status: string;
  estimatedAudience: number;
  queuedCount: number;
  sentCount: number;
  failedCount: number;
  suppressedCount: number;
  channel?: "telegram" | "email";
  scheduledFor?: string;
  createdAt: string;
  startedAt?: string;
  completedAt?: string;
};

export type Suppression = {
  customerId: string;
  reason: string;
  note?: string;
  createdAt: string;
};

export type ReferralProgram = {
  enabled: boolean;
  currency: string;
  inviterRewardMinor: number;
  inviteeRewardMinor: number;
  /** Rewards in subscription days; for a side with both, days win. */
  inviterRewardDays: number;
  inviteeRewardDays: number;
  qualification: string;
  inviterRewardCap?: number;
  attributionValidityDays: number;
  rewardExpiryDays?: number;
  termsUrl?: string;
  updatedAt?: string;
  /** A share of every referee payment, in basis points; see referral-commission.tsx. */
  commissionBps?: number;
  firstPaymentCommissionBps?: number;
  commissionPaymentCap?: number;
  commissionMinPaymentMinor?: number;
  commissionTriggers?: string[];
  commissionTiers?: { referrals: number; bps: number }[];
  allowRewardChoice?: boolean;
  /** What the current configuration has actually produced. */
  record: {
    attributed: number;
    qualified: number;
    rejected: number;
    rewardedMinor: number;
  };
};
