# Claude repository guide

Read and follow [`AGENTS.md`](./AGENTS.md) as the canonical repository instructions,
and [`REPO_GRAPH.md`](./REPO_GRAPH.md) for where things are: processes, packages,
layers, generated artifacts, and which files a given change starts in.

Before changing code:

1. Read the relevant Mintlify pages under `docs/` — the owner guides in
   `docs/features/` first, then the reference page they link to.
2. Confirm the Remnawave/Omniflow ownership boundary
   ([decisions](./docs/architecture/decisions.mdx)).
3. Identify transaction, idempotency, privacy, and migration implications.

Do not duplicate rules here. If a repository rule changes, update `AGENTS.md` and
link the relevant documentation. This file only adds the current state of the
project and the practical knowledge that keeps a session from tripping over the
tooling.

## Current state (22 November 2026)

- Omniflow replaced a Bedolaga fork running a live VPN business on 22 November;
  Bedolaga's pages, tickets, consents, promo groups (as the loyalty programme)
  and the rest of its history (`legacy_records`) are carried over. Migration
  tooling: `cmd/migrate-bedolaga`, `deploy/migration/00…07-*.sh`, and
  `docs/operations/migrate-from-bedolaga.mdx`.
- Production runs `main`, not releases: every commit on `main` that passes CI
  is built as `sha-<commit>` edge images (`.github/workflows/edge.yml`) and
  rolled out within about ten minutes by the server's autodeploy timer, which
  rolls back and alerts the operator group on a failed migration or readiness.
  Keep `main` green and do not push half-finished behaviour; the integration
  suite is not part of that gate, so run the affected tests before pushing.
  Releases (release-please) still cut versions, and each published release
  is announced in the 📣 Обновления topic of the Omniflow Community chat on
  Telegram by `.github/workflows/announce.yml` (called from release-please;
  dispatch it with `dry_run` to preview). Do not post release notes there by
  hand.
- The customer panel talks to the Telegram Mini App through tma.js
  (`apps/web/lib/tma.ts`), and Telegram avatars are stored in
  `telegram_avatars` and served by the API, never loaded from Telegram's hosts.
- Parity with Bedolaga is no longer tracked: Omniflow builds its own product,
  and a Bedolaga feature is not a reason to add one. A port that duplicates
  something the general model already covers gets removed (the "simple
  subscription" went on 24 November). Eight Bedolaga features never ported are
  listed in `BACKLOG.md` under «Было у Bedolaga, у нас нет»: QR payment in the
  bot, Overpay `.p12` upload, embedded payment forms, ban-system, Abuse API,
  ABAC, a log viewer, a live menu editor. Do not start one unasked. The `.p12` upload stays open on
  purpose: it would store a payment secret in the database, against the rule
  below; the owner chose to leave it rather than change the rule.
- Steam top-ups (`steam_topup`, bot only) sell through PlayWallet or Nervixy, chosen per
  product. A provider exists only while its key is in the environment
  (`APP_PLAYWALLET_API_KEY`, `APP_NERVIXY_API_KEY`); production has neither, so the
  feature is off and invisible there. DesslyHub was asked for too, but its public
  examples cover only Steam gifts, not wallet top-ups — not built until its top-up API
  is documented.
- Targets: Remnawave 3.4.x API, PostgreSQL 19, Valkey 9, Go with River jobs,
  Next.js 16 with next-intl.
- Exists in code but not reachable from any screen yet — say so instead of
  documenting it as working: AI drafting, copilot, and the MCP client (only the
  provider connection test reaches a provider; the first-party MCP server is
  served read-only at `/v1/mcp` to named tokens); a panel screen for required channels;
  creating plans and editing plan texts (API only); provider credentials
  (environment only).
- Web catalogues: `en` and `ru` are complete; `uk`, `zh`, `fa` trail them by
  about 150 keys and fall back. New copy goes into all five
  `apps/web/messages/*.json`; bot copy lives in `internal/botapp/messages*.go`.
- Docs: owner guides in `docs/features/`, reference pages by area, the sixteen
  architecture decisions on one page. Every page has a Russian mirror.

## Practical notes

- Commits go to `main` directly, Conventional Commits, author
  `kewldan <kewldanil1@gmail.com>`, no assistant attribution of any kind.
- Codegen after a contract or query change: `go tool sqlc generate`,
  `go tool oapi-codegen -config api/oapi-codegen.yaml api/openapi.yaml`,
  `bun run generate:api`. Orval exits non-zero on a Biome warning about a large
  file; `biome.json` raises `files.maxSize` for that reason.
- Migrations: write the file, `atlas migrate hash --dir file://database/migrations`,
  apply to a real PostgreSQL, then `sh tools/schema-snapshot.sh`. Never write
  `atlas.sum` yourself.
- The integration suite takes 12–13 minutes locally with Docker. It is not in
  `CI`: `.github/workflows/integration.yml` runs it nightly and by
  `gh workflow run integration.yml --ref main`, which the release process
  requires before merging a release PR. Run the affected tests locally before
  pushing a backend change. A newer push cancels the running CI.
- The web bundle budget (`bun run build && bun run perf:budget`) caps admin
  routes at 1300 kB and account routes at 1075 kB; heavy panel cards are loaded
  with `next/dynamic`.
- `next dev` writes `apps/web/AGENTS.md` and touches `apps/web/next-env.d.ts`;
  do not commit the `next-env.d.ts` change.
- Agents started with `isolation: "worktree"` branch from the HEAD at session
  start, not from the current one: tell them to `git checkout -B <branch> <sha>`
  first, and bring their work back by cherry-pick.
- In zsh, `$var:a` is a path modifier; write `${var}` before a colon.
- Payment, Fragment, and Telegram secrets live only in environment variables.
  Never store, log, or echo them, and never make a real Fragment purchase.
