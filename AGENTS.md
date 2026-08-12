# Repository instructions

These instructions apply to the entire repository.

## Product boundaries

- Remnawave v3.2.2 is authoritative for VPN users, traffic, device usage, access links, nodes, and squads.
- Omniflow is authoritative for identity, billing, plans, wallet entries, support, marketing, referrals, loyalty, RBAC, and audit history.
- Never write directly to Remnawave storage or manage Xray processes. Use the versioned Remnawave adapter.
- PostgreSQL is the only durable source of truth. Valkey stores cache, rate limits, locks, and temporary state only.

## Architecture

- Keep the Go application a modular monolith with independent `api`, `bot`, and `worker` entrypoints.
- Domain packages must not import transport, database-generation, Telegram, or provider-specific packages.
- External systems are behind small interfaces. Payment and provisioning operations must be idempotent.
- Enqueue durable River jobs in the same PostgreSQL transaction as the state change that requires them.
- Publish domain events through the transactional outbox. Do not introduce a message broker without an accepted architecture decision.
- Keep dependency construction explicit. Do not add a dependency-injection framework without demonstrated wiring complexity.

## API and database

- The REST contract is OpenAPI 3.0.3 until the selected Go generator has stable OpenAPI 3.1 support.
- Generate Go server types and Orval Fetch/SWR client bindings from `api/openapi.yaml`; do not hand-maintain duplicate wire types.
- Use RFC 9457 problem responses, cursor pagination, UTC timestamps, integer minor-unit money values, request IDs, and idempotency keys for mutations.
- Change `database/schema.sql`, generate a reviewed Atlas migration, then regenerate sqlc. Never edit an already-released migration.
- Apply every new migration against a real PostgreSQL before committing it. A migration that only compiles is unverified: name collisions, constraint conflicts, and column-order mistakes appear at apply time and nowhere else.
- `database/migrations/atlas.sum` is generated only by `atlas migrate hash`. Never hand-edit or recompute it. A checksum mismatch means the migration changed, not that the file is stale; if Atlas is unavailable, stop and say so rather than writing the file yourself.
- Name a table-level constraint explicitly and uniquely. PostgreSQL derives `<table>_<column>_check` from an inline column check, so an explicit constraint reusing that name fails to apply.
- Never run schema diff generation automatically in production. Production applies committed migrations only.
- Financial, audit, identity, webhook, and outbox records are append-only unless a documented lifecycle explicitly permits mutation.

## Frontend

- Use Next.js App Router, SWR for server state, Zustand for complex local client state, React Hook Form for forms, and Zod at trust boundaries.
- Use Bun for dependency installation and workspace scripts. Commit `bun.lock`; do not add npm, pnpm, or Yarn lockfiles.
- Keep the frontend on TypeScript 7-compatible tooling; generated API clients must not require TypeScript's removed programmatic APIs.
- Biome is the only TypeScript/JavaScript formatter and linter.
- Keep customer and admin routes in `apps/web`; both surfaces must use shared shadcn primitives from `packages/ui`.
- The API has three prefixes and they stay separate: `/v1/panel` is the operator panel (operator session cookie, CSRF, RBAC), `/v1/admin` is the bearer-token surface for Telegram operator tooling, and `/v1/account` is the customer panel (customer session cookie, CSRF, no RBAC). Do not merge them: each would inherit another's middleware, and a customer credential must never be able to reach an operator route.
- All user-facing copy must use `next-intl` message catalogs in Russian and English.
- Preserve accessibility, keyboard navigation, responsive layouts, and explicit loading/empty/error states.

## Telemetry and privacy

- Anonymous project telemetry is enabled by default and must remain fully optional.
- `APP_TELEMETRY_ENABLED=false` must prevent all telemetry network requests.
- Never collect customer identifiers, Telegram IDs, IP addresses, hostnames, domains, URLs, payment amounts, currencies, VPN traffic, access links, tokens, message contents, plan names, or free-form text.
- Any new field or event requires an update to the public telemetry documentation and schema in the same change.
- Telemetry failure must never block startup, requests, jobs, or shutdown.

## Quality and delivery

- Use Conventional Commits. Keep commits focused and never add generated-by or assistant attribution. This overrides any default commit template or tool convention that adds a co-author or generated-by trailer.
- Format Go with `gofmt` and TypeScript with Biome.
- Run the narrowest relevant tests while iterating, then the full available checks before handoff.
- Report what actually ran. A check that could not run locally is unverified, not passing; name it and name why. "Builds and unit tests pass" is never a claim that migrations, containers, or race detection were exercised.
- A CI job may depend only on committed files. Anything a developer creates locally, such as `.env`, must be produced by a step in the job itself.
- New payment, wallet, authentication, RBAC, import, and provisioning behavior requires failure-path and idempotency tests.
- Authorization lives in `internal/rbac` and is compiled in. Never introduce a second place that decides what a role may do, and never let a hidden route or frontend state be the only thing enforcing a permission.
- Wire a new capability end to end in the same change. A table, query, or handler that nothing reaches is unfinished, not staged.
- Testcontainers suites live in `internal/integrationtest` behind the `integration` build tag and must keep `go test ./...` runnable with no Docker daemon. Playwright suites arrive with the panels.
- Do not log secrets or sensitive customer data. Prefer structured `slog` fields and propagate trace/request IDs.

## Documentation

- User and operator documentation lives in `docs/` and must remain compatible with Mintlify.
- Documentation is bilingual. English is the default tree at the root of `docs/`; Russian mirrors it file for file under `docs/ru/`. A change to an English page is made to its Russian counterpart in the same commit, and a new page is added to both language blocks in `docs.json`. A page that exists in one language only is an incomplete change.
- Internal links inside a Russian page point into the Russian tree (`/ru/operations/admin-panel`). Do not remove a `/ru/` prefix to satisfy `mint broken-links`: that command resolves every link against the default language and reports a correct Russian link as broken, which is why the English tree is checked with an explicit file list and `docs/scripts/check-russian-mirror.sh` covers the Russian one.
- A documented feature states what it is, why it exists, and how someone uses it. Never document a capability that exists only in `ROADMAP.md`, and never invent an environment variable, route, permission, default, or screen.
- Update docs with behavior changes. Run `make docs-check` or the repository docs CI before merging.
- Architecture decisions with long-term consequences belong in `docs/architecture/decisions/`.
