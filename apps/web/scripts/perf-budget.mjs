/**
 * The front-end performance budget.
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
 * JavaScript is not the whole download, so it is not the whole budget. Two other
 * things ship from this build and sit on the critical path of every route, and
 * neither was measured until they were added here:
 *
 * - **The stylesheet.** One Tailwind file is loaded by every page and blocks
 *   rendering until it arrives. A careless `@import`, an unpurged utility set, or
 *   a component library pulled in for one screen moves it for all of them.
 * - **The self-hosted fonts.** They block text from painting in its real face,
 *   and adding a weight nobody asked for is a change nobody sees in review — the
 *   page looks identical and costs another download on every cold visit.
 *
 * Images, server response time, and field core web vitals are still deliberately
 * not measured here. They need a running installation with real content and real
 * clients, and a number invented from a local build would be worse than no
 * number — it would read as a gate that had passed. That is a stated limit
 * rather than an oversight: what this file gates is everything the build itself
 * can tell the truth about.
 */
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
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
// heaviest is /account/privacy at just over 1050, because it is the one customer
// screen carrying React Hook Form, which AGENTS.md mandates for forms. A budget
// has to accommodate the stack the repository requires — otherwise it is not a
// budget, it is an argument with the house rules that somebody eventually wins
// by deleting the check.
//
// The customer ceiling moved from 1050 to 1075 when the panel gained a desktop
// layout. The shell is in every account bundle, so a persistent sidebar with a
// sign-out control is charged to all of them: it cost 5 kB, and it was 5 kB the
// old ceiling did not have. What was avoidable was removed first rather than
// waved through — the secondary navigation carries no icons for exactly this
// reason — and what remains is the feature itself.
const BUDGETS = [
  { limitKb: 1075, prefix: "/account" },
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

/**
 * Budgets for the assets every route loads regardless of which one it is.
 *
 * These are whole-payload ceilings rather than per-route ones, because a
 * stylesheet and a font are not split per route: every page pays for all of
 * them. The headroom is set from where the build sits today plus room for a
 * legitimate addition — a genuinely new screen's worth of utilities, or one more
 * font weight — and not more, because a ceiling nothing can reach is not a
 * ceiling.
 */
const ASSET_BUDGETS = [
  { extensions: [".css"], label: "stylesheets", limitKb: 90 },
  { extensions: [".woff", ".woff2"], label: "fonts", limitKb: 200 },
];

const staticRoot = fileURLToPath(new URL("../.next/static", import.meta.url));

/** Every file under a directory, recursively, as absolute paths. */
function filesUnder(directory) {
  const found = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      found.push(...filesUnder(path));
    } else if (entry.isFile()) {
      found.push(path);
    }
  }
  return found;
}

let staticFiles;
try {
  staticFiles = filesUnder(staticRoot);
} catch (error) {
  console.error("Cannot read .next/static. Run `bun run build` first.");
  console.error(String(error));
  process.exit(2);
}

const assetFailures = [];
console.log("");
for (const budget of ASSET_BUDGETS) {
  const matching = staticFiles.filter((path) =>
    budget.extensions.some((extension) => path.endsWith(extension)),
  );
  const kb = matching.reduce((total, path) => total + statSync(path).size, 0) / 1024;
  const over = kb > budget.limitKb;
  console.log(
    `${over ? "FAIL" : "ok  "} ${kb.toFixed(0).padStart(6)} kB / ${budget.limitKb} kB  ` +
      `${budget.label} (${matching.length} file${matching.length === 1 ? "" : "s"})`,
  );
  if (over) {
    assetFailures.push({ kb, ...budget });
  }
  // A budget that silently passes because the files moved is not a budget. Fonts
  // are the likely case: a change to how they are loaded could leave none in the
  // build, and "0 kB, within budget" would read as an improvement.
  if (matching.length === 0) {
    console.error(`  no ${budget.label} were found in the build; the budget measured nothing`);
    assetFailures.push({ kb, ...budget });
  }
}

if (assetFailures.length > 0) {
  console.error(`\n${assetFailures.length} asset budget(s) failed.`);
  process.exit(1);
}
console.log("\nStylesheets and fonts are within budget.");
