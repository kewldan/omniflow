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

## Current state (21 November 2026)

- Omniflow replaces a Bedolaga fork that runs a live VPN business. Migration
  tooling: `cmd/migrate-bedolaga`, `deploy/migration/00…07-*.sh`, and
  `docs/operations/migrate-from-bedolaga.mdx`.
- Parity with Bedolaga is tracked in `bedolaga-bot-gaps.md`: 226 closed, none
  partial, 10 open, 8 declined. The open ten are listed in `BACKLOG.md` under
  «Остаток паритета с Bedolaga»: QR payment in the bot, Overpay INT, Overpay
  `.p12` upload, embedded payment forms, ban-system, Abuse API, ABAC, a log
  viewer, bot button styles, a live menu editor. Do not start one unasked.
- Targets: Remnawave 3.4.x API, PostgreSQL 19, Valkey 9, Go with River jobs,
  Next.js 16 with next-intl.
- Exists in code but not reachable from any screen yet — say so instead of
  documenting it as working: AI drafting, copilot, and the MCP server
  (`internal/mcpserver` is not started by any process; only the provider
  connection test reaches a provider); a panel screen for required channels;
  creating plans and editing plan texts (API only); provider credentials
  (environment only).
- Web catalogues: `en` and `ru` are complete; `uk`, `zh`, `fa` trail them by
  about 150 keys and fall back. New copy goes into all five
  `apps/web/messages/*.json`; bot copy lives in `internal/botapp/messages*.go`.
- Docs: owner guides in `docs/features/`, reference pages by area, the fifteen
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
- The integration suite takes 12–13 minutes locally with Docker; CI runs it with
  `-timeout=50m`. A newer push cancels the running CI.
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
