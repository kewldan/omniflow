import "server-only";

/**
 * The operator's own advertising measurement, read on the server.
 *
 * Read here rather than in a client component for the same reason branding is:
 * a verification tag has to be in the document a webmaster tool fetches, and a
 * tool fetching a page that only grows its tag after hydration sees a page with
 * no tag on it.
 *
 * The counters are read here too but not rendered here. This says what *would*
 * run; a client component decides whether it does, because that decision
 * depends on a consent choice that lives in the visitor's browser.
 */

export type AnalyticsVerification = { name: string; content: string };

export type PublicAnalytics = {
  /**
   * False when nothing would run even with consent — which suppresses the
   * consent request entirely. Asking somebody to agree to nothing is worse
   * than not asking.
   */
  measurable: boolean;
  /** Identifier per provider. Present only when the operator enabled measurement. */
  counters?: Record<string, string>;
  /** Rendered regardless of consent: they set no cookie and observe nobody. */
  verifications?: AnalyticsVerification[];
};

const API_BASE = process.env.API_INTERNAL_URL ?? process.env.NEXT_PUBLIC_API_URL ?? "";

/** Nothing configured, which is what a fresh installation has. */
const NONE: PublicAnalytics = { measurable: false };

export async function readAnalytics(): Promise<PublicAnalytics> {
  if (!API_BASE) {
    return NONE;
  }
  try {
    const response = await fetch(`${API_BASE}/v1/analytics`, { next: { revalidate: 300 } });
    if (!response.ok) {
      return NONE;
    }
    const payload = (await response.json()) as Partial<PublicAnalytics>;
    return {
      measurable: payload.measurable === true,
      counters: payload.counters ?? undefined,
      verifications: payload.verifications ?? undefined,
    };
  } catch {
    // A page must render without this. An installation whose analytics cannot
    // be read has a page with no counter on it, not a page that fails.
    return NONE;
  }
}
