import AxeBuilder from "@axe-core/playwright";
import type { Page } from "@playwright/test";

/**
 * The accessibility gate, as one function every spec can call on a page it
 * has already reached.
 *
 * It ran only on the three pages a visitor sees signed out, which is the part
 * of the product with the least in it. The signed-in sweeps in the customer
 * and operator journeys reach every panel page anyway, so they run it too.
 *
 * WCAG 2.2 AA, which is what the release gate asks for; the tags are a
 * superset, so all four levels are named. Serious and critical only: gating on
 * every advisory rule produces a list nobody reads and a gate somebody disables.
 *
 * Returns one line per violation, prefixed with the page, so a sweep can
 * collect them all and fail once with the whole list.
 */
export async function seriousViolations(page: Page, label = page.url()): Promise<string[]> {
  const results = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22a", "wcag22aa"])
    .analyze();
  return results.violations
    .filter((violation) => violation.impact === "serious" || violation.impact === "critical")
    .map(
      (violation) =>
        `${label}: ${violation.id} — ${violation.help} (${violation.nodes
          .slice(0, 3)
          .map((node) => node.target.join(" "))
          .join(", ")})`,
    );
}
