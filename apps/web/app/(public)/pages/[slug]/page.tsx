import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { getLocale, getTranslations } from "next-intl/server";

import { InfoDocumentView } from "@/components/info-document";
import { readPage } from "@/lib/pages";

type Params = { params: Promise<{ slug: string }> };

/**
 * One published document at a stable address.
 *
 * The address is the feature. A payment provider approves a URL for an offer or
 * a privacy policy, so the slug is the page's identity in the database rather
 * than a field beside a generated one, and a page is withdrawn by unpublishing
 * rather than by moving.
 */
export async function generateMetadata({ params }: Params): Promise<Metadata> {
  const { slug } = await params;
  const page = await readPage(slug, await getLocale());
  if (!page) {
    return {};
  }
  return { title: page.title };
}

export default async function PublishedPage({ params }: Params) {
  const { slug } = await params;
  const locale = await getLocale();
  const [page, translate] = await Promise.all([readPage(slug, locale), getTranslations("pages")]);

  if (!page) {
    // A draft, a deleted page, and an unreachable API are one answer here.
    // Distinguishing them would tell an anonymous visitor whether a given
    // address exists as a draft.
    notFound();
  }

  return (
    <article className="space-y-6">
      <header className="space-y-1">
        <h1 className="font-semibold text-xl tracking-tight">{page.title}</h1>
        <p className="font-mono text-[11px] text-subtle-foreground">
          {translate("updated", { date: new Date(page.updatedAt).toLocaleDateString(locale) })}
        </p>
      </header>
      <InfoDocumentView document={page.document} />
    </article>
  );
}
