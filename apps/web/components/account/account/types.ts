/**
 * The wire shapes of the referral, loyalty, contact, and privacy routes.
 *
 * They are written by hand rather than imported from the generated client for
 * the same reason the rest of the customer panel does it: Orval's SWR client
 * emits bare `fetch` calls and is not what the panel actually talks through, so
 * the types are kept beside the screens that consume them and mirror
 * `internal/httpapi/accountreferral.go` field for field. A field that is absent
 * from that handler is absent here.
 */

/** The operator-configured invite scheme, exactly as the customer is promised it. */
export type ReferralProgram = {
  enabled: boolean;
  currency: string;
  inviterRewardMinor: number;
  inviteeRewardMinor: number;
  /** What has to happen before a reward is granted; one of a closed set. */
  qualification: string;
  attributionValidityDays: number;
  /** Null when the operator set no ceiling on how many invites may pay out. */
  inviterRewardCap: number | null;
  rewardExpiryDays: number | null;
  termsUrl?: string;
};

/** The three words the API uses for "is this money mine?". */
export type RewardState = "pending" | "qualified" | "rejected";

export type ReferralReward = {
  id: string;
  role: string;
  state: RewardState;
  amountMinor: number;
  currency: string;
  grantedAt: string;
  reversedAt?: string;
};

export type ReferralRewardPage = {
  items: ReferralReward[];
  /** Empty on the last page. */
  nextCursor: string;
};

/**
 * Why no shareable link could be built.
 *
 * The panel renders the code and this reason instead of a link, because a share
 * control that copies a URL going nowhere only fails after the customer has
 * already sent it to somebody.
 */
export type ReferralLinkReason = "public_url_not_configured" | "no_code";

export type ReferralSummary = {
  program: ReferralProgram;
  code: string;
  link: string;
  linkAvailable: boolean;
  linkReason?: ReferralLinkReason;
  invited: number;
  qualified: number;
  pending: number;
  rejected: number;
  /** What the customer kept, with reversals already excluded. */
  rewardedMinor: number;
  /** What was granted and later taken back, carried separately so a drop is explainable. */
  reversedMinor: number;
  rewardCount: number;
  currency: string;
  /** Null when there is no cap to count down from. */
  remainingSlots: number | null;
  rewards: ReferralRewardPage;
};

export type LoyaltyMetric = "spend" | "tenure" | "orders";

export type LoyaltyTier = {
  code: string;
  /**
   * Both names arrive because the server caches one response for every reader.
   * The panel already knows the locale, so it picks; resolving upstream would
   * make a cached tier name wrong for the next customer.
   */
  nameEn: string;
  nameRu: string;
  threshold: number;
  discountBps: number;
  current: boolean;
};

export type LoyaltyRules = {
  metric: LoyaltyMetric;
  currency: string;
  windowDays: number;
  graceDays: number;
  version: number;
};

/**
 * The loyalty response, as a union rather than one shape with optional fields.
 *
 * "The programme is off" and "you are on the bottom rung" are different answers
 * and the API keeps them apart; collapsing them into one nullable object here
 * would put them back together and let a screen render an empty ladder as
 * though it meant something.
 */
export type LoyaltyStanding =
  | { enabled: false }
  | {
      enabled: true;
      rules: LoyaltyRules;
      tiers: LoyaltyTier[];
      /** False until the evaluation worker has placed this customer. */
      evaluated: boolean;
      tier?: LoyaltyTier;
      next?: LoyaltyTier;
      metric?: number;
      remaining?: number;
      percent?: number;
      evaluatedAt?: string;
      /** Set while a tier is being held through grace rather than earned. */
      graceUntil?: string;
    };

export type ContactKind = "email" | "phone" | "telegram";

export type ContactChannel = {
  id: string;
  kind: ContactKind;
  /** Empty when the stored value could not be decrypted, which is not a failure of the list. */
  value: string;
  verified: boolean;
  transactional: boolean;
  marketing: boolean;
  createdAt: string;
};

export type ConsentRecord = {
  purpose: string;
  granted: boolean;
  policyVersion: string;
  source: string;
  occurredAt: string;
};

export type DeletionState = {
  pending: boolean;
  requestedAt: string | null;
  cancelledAt: string | null;
  reason: string;
  /** Always `operator_retention_workflow`: the request is carried out elsewhere. */
  executedBy: string;
};

export type PrivacyOverview = {
  retention: {
    status: string;
    suspendedAt: string | null;
    deletedAt: string | null;
    anonymizedAt: string | null;
    retentionUntil: string | null;
  };
  deletion: DeletionState;
  consents: { current: Record<string, boolean>; history: ConsentRecord[] };
  /** What an export would contain, described before one is produced. */
  export: { sections: string[]; redactions: string[]; contactValuesAvailable: boolean };
};

/**
 * The export document.
 *
 * Only the fields the panel reads are named. The rest of the document is passed
 * through to the saved file untouched, so a section added on the server reaches
 * the customer without a frontend change — and `redactions` and `truncated` are
 * typed because the screen has to show them rather than let them pass silently.
 */
export type ExportDocument = {
  version: string;
  generatedAt: string;
  redactions: string[];
  truncated: string[];
  [section: string]: unknown;
};
