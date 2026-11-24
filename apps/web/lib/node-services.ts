/**
 * Sites whose answer depends on where a server's exit address is placed, as
 * Remnawave's geocheck found them on each node. Shared by the customer's
 * servers screen and the operator's hosts table.
 */
export const NODE_SERVICES = [
  "gemini",
  "youtube",
  "reddit",
  "chatgpt",
  "claude",
  "netflix",
  "spotify",
  "tiktok",
] as const;

export type NodeService = (typeof NODE_SERVICES)[number];

/** Product names, the same in every language. */
export const NODE_SERVICE_LABELS: Record<NodeService, string> = {
  chatgpt: "ChatGPT",
  claude: "Claude",
  gemini: "Gemini",
  netflix: "Netflix",
  reddit: "Reddit",
  spotify: "Spotify",
  tiktok: "TikTok",
  youtube: "YouTube",
};

/**
 * yes opens, no is refused (Reddit asking to sign in counts), limited opens
 * but not as the server's country would — YouTube's Russian region, Netflix
 * with its own shows only.
 */
export type ServiceState = "yes" | "no" | "limited";
export type ServiceStatus = { state: ServiceState; region?: string };
export type NodeServices = Partial<Record<NodeService, ServiceStatus>>;

/** A set of servers' answers: "some" when the servers disagree. */
export type SummaryState = ServiceState | "some";

/**
 * Summarizes several servers service by service, in display order. Services
 * no server's check settled are left out; a single server keeps its region.
 */
export function summarizeServices(
  servers: { services?: NodeServices }[],
): { service: NodeService; state: SummaryState; region?: string }[] {
  const summary: { service: NodeService; state: SummaryState; region?: string }[] = [];
  for (const service of NODE_SERVICES) {
    const statuses = servers
      .map((server) => server.services?.[service])
      .filter((status): status is ServiceStatus => status !== undefined);
    if (statuses.length === 0) {
      continue;
    }
    const states = new Set(statuses.map((status) => status.state));
    const regions = new Set(statuses.map((status) => status.region ?? ""));
    const [only] = states;
    summary.push({
      region: regions.size === 1 ? statuses[0]?.region : undefined,
      service,
      state: states.size === 1 && only ? only : "some",
    });
  }
  return summary;
}
