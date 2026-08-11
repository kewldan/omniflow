/**
 * The shapes the AI and MCP settings API returns.
 *
 * No type here has a field for a credential. That is not an oversight the
 * client works around — the API does not return one, and mirroring that in the
 * types means a component cannot render a secret even by accident. What comes
 * back instead is `keyConfigured` or `credentialConfigured`, which is the only
 * safe rendering of a stored secret.
 */

export type AiProvider = {
  slug: string;
  kind: "openai_compatible" | "anthropic" | "gemini";
  displayName: string;
  baseUrl?: string;
  enabled: boolean;
  zeroRetention: boolean;
  trainsOnData: boolean;
  retentionNote?: string;
  dataRegion?: string;
  keyConfigured: boolean;
  lastCheckedAt?: string;
  lastCheckOk: boolean;
  lastCheckError?: string;
};

/**
 * A warning is computed by the server at read time rather than stored, so it
 * cannot go stale against the provider it describes.
 */
export type AiWarning = {
  code: string;
  text: string;
  blocking: boolean;
};

export type AiFeature = {
  feature: string;
  enabled: boolean;
  provider?: string;
  model?: string;
  maxTokens?: number;
  timeoutMs?: number;
  budgetTokens?: number;
  budgetWindowSeconds?: number;
  budgetCostMinor?: number;
  retainPrompts: boolean;
  retainOutputs: boolean;
  retentionDays: number;
  warnings: AiWarning[];
  updatedAt: string;
};

export type AiFeatureListing = {
  items: AiFeature[] | null;
  /** Every feature this build supports, so the screen renders the full set. */
  available: string[] | null;
};

export type AiUsageLimit = {
  id?: string;
  scope: "installation" | "role" | "operator" | "feature";
  ref?: string;
  feature?: string;
  windowSeconds: number;
  maxRequests?: number;
  maxTokens?: number;
  maxCostMinor?: number;
};

export type AiUsageRow = {
  feature: string;
  provider: string;
  model: string;
  requests: number;
  inputTokens: number;
  outputTokens: number;
  estimatedCostMinor: number;
  meanLatencyMs: number;
  p95LatencyMs: number;
  failures: number;
};

export type AiUsageReport = {
  since: string;
  until: string;
  items: AiUsageRow[] | null;
};

export type McpServer = {
  slug: string;
  displayName: string;
  endpoint: string;
  enabled: boolean;
  allowedHosts: string[] | null;
  allowPrivateNetwork: boolean;
  timeoutMs: number;
  maxResponseBytes: number;
  maxCallsPerRequest: number;
  maxDepth: number;
  costLimitMinor?: number;
  credentialConfigured: boolean;
  protocolVersion?: string;
  serverName?: string;
  serverVersion?: string;
  discoveredAt?: string;
  lastCheckedAt?: string;
  lastCheckOk: boolean;
  lastCheckError?: string;
  consecutiveFailures: number;
};

export type McpTool = {
  server: string;
  tool: string;
  enabled: boolean;
  permission: string;
  writes: boolean;
  description?: string;
  /** False when this build cannot enforce the declared schema. */
  schemaUsable: boolean;
  schemaProblem?: string;
};

export type McpEvent = {
  id: string;
  occurredAt: string;
  kind: string;
  server?: string;
  tool?: string;
  operatorId?: string;
  confirmed: boolean;
  reason?: string;
  outcome: "allowed" | "refused" | "failed" | "replayed";
  detail?: string;
  responseBytes: number;
  durationMs: number;
  findings: string[] | null;
};

export type McpEventPage = {
  items: McpEvent[] | null;
  nextCursor?: string;
};
