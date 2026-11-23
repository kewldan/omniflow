import Link from "next/link";
import { getLocale, getTranslations } from "next-intl/server";

import { readPages } from "@/lib/pages";

export default async function PagesIndex() {
  const locale = await getLocale();
  const translate = await getTranslations("pages");
  // The documents list is the operator's legal pages. Frequently asked
  // questions are help, not a document, and are reached from support.
  const pages = (await readPages(locale)).filter((page) => page.kind !== "faq");

  return (
    <div className="space-y-6">
      <h1 className="font-semibold text-xl tracking-tight">{translate("title")}</h1>
      {pages.length === 0 ? (
        <p className="text-muted-foreground text-sm">{translate("empty")}</p>
      ) : (
        <ul className="space-y-2">
          {pages.map((page) => (
            <li key={page.slug}>
              <Link
                className="block rounded-lg border border-border bg-card p-3.5 text-[13.5px] hover:border-accent"
                href={`/pages/${page.slug}`}
              >
                {page.title}
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
