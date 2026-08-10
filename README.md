# Omniflow

Open-source billing, support, marketing, and self-service platform for VPN services managed by Remnawave.

Omniflow keeps Remnawave authoritative for VPN users, traffic, devices, access links, and squads. Omniflow owns identities, plans, orders, payments, wallet credits, support, campaigns, referrals, loyalty, RBAC, and audit history.

## Status

Omniflow is under active development. The repository currently contains the production-oriented foundation and contracts; user-facing product modules will land incrementally.

## Stack

- Go 1.26, Chi, pgx, sqlc, River, Valkey, OpenAPI, OpenTelemetry
- PostgreSQL 18 with Atlas versioned migrations
- Next.js 16.3, React, TypeScript 7, SWR, Zustand, Tailwind CSS v4, shadcn/ui
- React Hook Form, Zod, next-intl
- Bun workspaces, Biome formatting/linting, and Orval-generated Fetch/SWR clients
- Docker Compose with optional reverse-proxy examples
- Mintlify documentation in [`docs`](./docs)

## Quick start

1. Copy `.env.example` to `.env` and set the required secrets.
2. Start the local stack:

   ```bash
   docker compose up --build
   ```

3. Open the API health endpoint at `http://localhost:8080/healthz`.

The admin and portal Compose profiles are optional during backend-first development.

## Telemetry

Anonymous installation telemetry is enabled by default to help prioritize features across the open-source community. It never includes customer identities, payment values, VPN traffic, domains, tokens, or message content. Disable all telemetry with:

```bash
OMNIFLOW_TELEMETRY_ENABLED=false
```

See [`docs/operations/telemetry.mdx`](./docs/operations/telemetry.mdx) for the exact payload and policy.

## Development

Common commands are documented by `make help`. Generated code and lockfiles are committed. Schema changes must be represented by reviewed Atlas migrations before `sqlc` generation.

Read [`CONTRIBUTING.md`](./CONTRIBUTING.md), [`AGENTS.md`](./AGENTS.md), and the [architecture documentation](./docs/architecture/overview.mdx) before making structural changes.

## Security

Do not report vulnerabilities in public issues. Follow [`SECURITY.md`](./SECURITY.md).

## License

Apache License 2.0. See [`LICENSE`](./LICENSE).
