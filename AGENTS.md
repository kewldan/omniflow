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
- Never run schema diff generation automatically in production. Production applies committed migrations only.
- Financial, audit, identity, webhook, and outbox records are append-only unless a documented lifecycle explicitly permits mutation.

## Frontend

- Use Next.js App Router, SWR for server state, Zustand for complex local client state, React Hook Form for forms, and Zod at trust boundaries.
- Use Bun for dependency installation and workspace scripts. Commit `bun.lock`; do not add npm, pnpm, or Yarn lockfiles.
- Keep the frontend on TypeScript 7-compatible tooling; generated API clients must not require TypeScript's removed programmatic APIs.
- Biome is the only TypeScript/JavaScript formatter and linter.
- Use shared shadcn-style primitives from `packages/ui`; avoid separate admin and portal design systems.
- All user-facing copy must use `next-intl` message catalogs in Russian and English.
- Preserve accessibility, keyboard navigation, responsive layouts, and explicit loading/empty/error states.

## Telemetry and privacy

- Anonymous project telemetry is enabled by default and must remain fully optional.
- `APP_TELEMETRY_ENABLED=false` must prevent all telemetry network requests.
- Never collect customer identifiers, Telegram IDs, IP addresses, hostnames, domains, URLs, payment amounts, currencies, VPN traffic, access links, tokens, message contents, plan names, or free-form text.
- Any new field or event requires an update to the public telemetry documentation and schema in the same change.
- Telemetry failure must never block startup, requests, jobs, or shutdown.

## Quality and delivery

- Use Conventional Commits. Keep commits focused and never add generated-by or assistant attribution.
- Format Go with `gofmt` and TypeScript with Biome.
- Run the narrowest relevant tests while iterating, then the full available checks before handoff.
- New payment, wallet, authentication, RBAC, import, and provisioning behavior requires failure-path and idempotency tests.
- Full Testcontainers and Playwright suites are planned for later milestones; new foundations must remain compatible with them.
- Do not log secrets or sensitive customer data. Prefer structured `slog` fields and propagate trace/request IDs.

## Documentation

- User and operator documentation lives in `docs/` and must remain compatible with Mintlify.
- Update docs with behavior changes. Run `mint validate` or the repository docs CI before merging.
- Architecture decisions with long-term consequences belong in `docs/architecture/decisions/`.
