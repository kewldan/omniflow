import "server-only";

import type { InfoDocument } from "@/components/info-document";

/**
 * The published information pages, read on the server.
 *
 * These are rendered server-side rather than fetched in the browser for a
 * specific reason: the readers who matter most are a payment provider's
 * reviewer, an application store's reviewer, and a search engine, and none of
 * them is guaranteed to run JavaScript. A terms page that needs hydration to
 * appear is a terms page that does not exist to the party requiring it.
 */

export type PageSummary = { slug: string; kind: string; title: string };

export type PublishedPage = {
  slug: string;
  kind: string;
  locale: string;
  title: string;
  document: InfoDocument;
  updatedAt: string;
};

const API_BASE = process.env.API_INTERNAL_URL ?? process.env.NEXT_PUBLIC_API_URL ?? "";

export async function readPages(locale: string): Promise<PageSummary[]> {
  if (!API_BASE) {
    return [];
  }
  try {
    const response = await fetch(
      `${API_BASE}/v1/pages?locale=${encodeURIComponent(locale)}`,
      // The API asks for five minutes; matching it here means a burst of
      // reviewers does not become a burst of queries.
      { next: { revalidate: 300 } },
    );
    if (!response.ok) {
      return [];
    }
    const payload = (await response.json()) as { items?: PageSummary[] };
    return payload.items ?? [];
  } catch {
    return [];
  }
}

export async function readPage(slug: string, locale: string): Promise<PublishedPage | null> {
  if (!API_BASE) {
    return null;
  }
  try {
    const response = await fetch(
      `${API_BASE}/v1/pages/${encodeURIComponent(slug)}?locale=${encodeURIComponent(locale)}`,
      { next: { revalidate: 300 } },
    );
    if (!response.ok) {
      // A draft, a deleted page, and an unreachable API all reach here. The
      // caller renders a 404 for all three: telling an anonymous visitor which
      // of the three it was would say whether a given address exists as a
      // draft, and there is no reader for whom that distinction is useful.
      return null;
    }
    return (await response.json()) as PublishedPage;
  } catch {
    return null;
  }
}
