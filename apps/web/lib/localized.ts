/**
 * Picks the operator-authored text that matches the interface language.
 *
 * Queues, tags, canned replies, segments, and offers are written twice, once
 * per language, and the panel used to print the English half regardless of
 * the language it was showing — a Russian-speaking desk saw English queue
 * names beside Russian labels. The other half is the fallback when the
 * matching one was left empty, and `fallback` (usually the stable code) when
 * both were.
 */
export function localized(
  locale: string,
  english: string | null | undefined,
  russian: string | null | undefined,
  fallback = "",
): string {
  const preferred = locale === "ru" ? russian : english;
  const other = locale === "ru" ? english : russian;
  return preferred?.trim() || other?.trim() || fallback;
}
