/**
 * The JavaScript performance budget.
 *
 * It runs against a completed `next build` and fails when a route's first-load
 * JavaScript exceeds its budget. First-load is the number that matters on a
 * phone over a slow connection: it is everything the browser must download,
 * parse, and execute before the page is interactive, and it is the one figure a
 * single careless import can move by a hundred kilobytes without anybody
 * noticing in review.
 *
 * The figures come from Next's own `route-bundle-stats.json` rather than being
 * recomputed from the chunk manifests. A budget that measures the build
 * differently from the build itself is a budget that argues with it, and the
 * argument is never resolved in the budget's favour.
 *
 * Sizes are **uncompressed** bytes, which is what that file reports. It is also
 * the more honest figure to gate on: compression hides a regression that still
 * costs the phone its parse and execute time, and parse time is what a mid-range
 * Android device actually runs out of.
 *
 * The budgets are per-area rather than global. The customer panel is opened on a
 * phone, frequently from inside Telegram, and is held to the tighter number; the
 * operator panel is a desktop workspace kept open all day, where a larger bundle
 * amortises over a long session. One global budget would be either too loose to
 * catch a customer-side regression or too tight to be honest about the admin one.
 *
 * Images, server response time, and field core web vitals are deliberately not
 * measured here. They need a running installation with real content and real
 * clients, and a number invented from a local build would be worse than no
 * number — it would read as a gate that had passed.
 */
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const statsPath = fileURLToPath(
  new URL("../.next/diagnostics/route-bundle-stats.json", import.meta.url),
);

/**
 * Budgets in kilobytes of uncompressed first-load JavaScript. The most specific
 * matching prefix wins, so a single heavy route can be given its own ceiling
 * without loosening the area around it.
 */
// The ceilings are set from where the routes actually sit, plus the headroom a
// legitimate change needs. Customer routes cluster between 850 and 970 kB; the
// heaviest is /account/privacy at just over 1000, because it is the one customer
// screen carrying React Hook Form, which AGENTS.md mandates for forms. A budget
// has to accommodate the stack the repository requires — otherwise it is not a
// budget, it is an argument with the house rules that somebody eventually wins
// by deleting the check.
const BUDGETS = [
  { limitKb: 1050, prefix: "/account" },
  { limitKb: 1300, prefix: "/admin" },
  { limitKb: 900, prefix: "/" },
];

function budgetFor(route) {
  return BUDGETS.filter((budget) => route.startsWith(budget.prefix)).sort(
    (left, right) => right.prefix.length - left.prefix.length,
  )[0];
}

let stats;
try {
  stats = JSON.parse(readFileSync(statsPath, "utf8"));
} catch (error) {
  console.error("Cannot read .next/diagnostics/route-bundle-stats.json.");
  console.error("Run `bun run build` first.");
  console.error(String(error));
  process.exit(2);
}

const report = stats
  .map((entry) => {
    const budget = budgetFor(entry.route);
    return {
      kb: entry.firstLoadUncompressedJsBytes / 1024,
      limitKb: budget.limitKb,
      route: entry.route,
    };
  })
  .sort((left, right) => right.kb - left.kb);

for (const entry of report.slice(0, 12)) {
  const marker = entry.kb > entry.limitKb ? "FAIL" : "ok  ";
  console.log(
    `${marker} ${entry.kb.toFixed(0).padStart(6)} kB / ${entry.limitKb} kB  ${entry.route}`,
  );
}

const failures = report.filter((entry) => entry.kb > entry.limitKb);
if (failures.length > 0) {
  console.error(`\n${failures.length} route(s) over the JavaScript budget:`);
  for (const entry of failures) {
    console.error(`  ${entry.route}: ${entry.kb.toFixed(0)} kB > ${entry.limitKb} kB`);
  }
  process.exit(1);
}
console.log(`\nAll ${report.length} routes are within the JavaScript budget.`);
