<!-- markdownlint-disable MD013 -->

# 🌊 Omniflow

## The open-source customer and operations platform for Remnawave VPN services

Build Telegram-first self-service, subscriptions, billing, support, marketing, and administration around your existing Remnawave installation—without taking ownership of Xray or duplicating VPN state.

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-0ea5e9.svg)](./LICENSE)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](./go.mod)
[![Next.js 16.3](https://img.shields.io/badge/Next.js-16.3-000000?logo=next.js&logoColor=white)](./apps/web/package.json)
[![TypeScript 7](https://img.shields.io/badge/TypeScript-7-3178C6?logo=typescript&logoColor=white)](./package.json)
[![Bun](https://img.shields.io/badge/Bun-1.3-fbf0df?logo=bun&logoColor=black)](./package.json)
[![Remnawave](https://img.shields.io/badge/Remnawave-3.2.2-8b5cf6)](https://docs.rw/api/)

[Quick start](#-quick-start) · [Roadmap](./ROADMAP.md) · [Documentation](./docs/index.mdx) · [Architecture](./docs/architecture/overview.mdx) · [Contributing](./CONTRIBUTING.md)

> [!IMPORTANT]
> Omniflow is under active development. Everything through v0.10 is implemented on `main` and verified against PostgreSQL 18: the platform foundation, the commerce backend, the complete Telegram customer product, the production runtime, and the whole operator panel. An operator can run the support desk, build audience segments and campaigns, configure the referral programme and loyalty tiers, require Telegram channel membership before a purchase, and configure every backend capability — branding, the Remnawave connection, the bot, the operator group, maintenance, notification thresholds, telemetry, backups, and session policy — without SQL or shell access. AI and MCP are optional, off until an owner configures them, enabled one feature at a time, and bounded per operator, role, feature, and installation. A shop order now carries the wallet, a promo code, and a saved cart through the same pipeline a plan uses, with a discount floored at what the provider charges and a saved shop purchase that re-quotes rather than buying itself. Campaign and notification test delivery and update-availability reporting are tracked in the roadmap's verification debt. v0.9 and v0.10 complete the customer web panel. A customer signs in at `/account` with Telegram, with an operator-enabled link the bot delivers, or through any number of OIDC providers, and reaches the same account the bot has been using. They can compare plans, buy, renew, upgrade, downgrade, apply a promo code and be told why it was refused, see the wallet applied to the minor unit, pick a payment provider and be handed off to it, watch provisioning finish, top up, read the ledger, buy Telegram Premium or Stars for somebody else, open a support ticket with an attachment, read announcements, set what they are willing to be messaged about, share a referral link, see a loyalty tier, export their own data, and request deletion — alongside the subscriptions, traffic, devices, connection instructions, access-link rotation, sessions, and account history v0.9 shipped. It is one product with two surfaces: a cart started in a chat is the cart the browser shows, and a support reply read on the web stops being unread in Telegram.

## ✨ Highlights

| | Capability | What it provides |
| --- | --- | --- |
| 🤖 | **Telegram-first experience** | Russian and English self-service with subscription security, device management, notifications, referrals, support tickets, and responsive inline navigation. |
| 🔗 | **Remnawave-native integration** | Targets the official Remnawave 3.2.2 API and keeps Remnawave authoritative for VPN users, traffic, devices, links, nodes, and squads. |
| 💳 | **Commerce-ready backend** | Immutable plan versions, promotions, provider-neutral payments, refunds, wallet ledger, entitlements, durable fulfillment, and drift recovery. |
| 🧭 | **One web application** | Customer routes and the `/admin` workspace share Next.js, localization, API bindings, and the same shadcn component system. |
| 🔐 | **Operator panel with real access control** | Argon2id passwords, TOTP with recovery codes, optional OIDC, dual-expiry rotating sessions, six built-in roles, and an append-only audit trail enforced in the API rather than by hidden routes. |
| 📊 | **An operations dashboard that explains itself** | Every tile publishes the definition of what it counted, times are UTC, and revenue is reported as three separate figures rather than one number that double-counts wallet credit. |
| 🛑 | **Risk signals that never punish anybody** | External blocklists are stored as salted digests, anomaly rules are operator-configured per metric, and every adverse decision is a human one with a mandatory reason and an audit event. |
| 🔔 | **Security notices where you already look** | Sign-ins from a new address, password changes, second factors removed, and owner grants reach the operator Telegram group — naming the event and the account, never an address or a token. |
| 🛡️ | **Privacy-conscious by design** | Protected Telegram messages, no HWID/IP display, no subscription-link storage, explicit secret boundaries, and optional anonymous telemetry. |
| 🧱 | **Production-oriented foundation** | PostgreSQL migrations, durable River jobs, transactional outbox, Valkey, generated contracts, structured logs, observability, and security automation. |
| 🎫 | **A support desk that is a desk** | Queues, assignment, priority, tags, SLA timestamps, internal notes distinct from customer-visible messages, canned responses, merge and reopen, and workload reporting with the definitions published beside the numbers. |
| 📣 | **Campaigns you can check before you send** | A segment's definition is rendered in words beside its live count, generated from the filters themselves so it cannot describe something the query does not do. Suppressed is reported beside sent, because not messaging somebody on purpose is not a failure. |
| 🧠 | **AI that is optional, grounded, and never in charge** | Off until an owner configures it, enabled per feature, redacted inside the gateway so a feature cannot forget. Answers cite records an operator can open, drafts stay drafts until a person accepts them, and abuse analysis recommends a look rather than taking an action. **No feature invokes a provider in this build** — the registry and the budgets are real, the surfaces that would use them are not mounted. |
| 🔌 | **MCP on both sides, with the same rules** | Connections are off until enabled and reach only allowlisted tools on named hosts; arguments and results are schema-validated, external writes need a confirmation and a reason, and everything a server returns is fenced as data. The first-party server authorises as the operator whose token was presented and makes every mutation idempotent. **Neither side is mounted in this build**, for the same reason. |
| 🔑 | **Secrets that are write-only by construction** | The listing queries do not select the ciphertext column, an omitted credential keeps the stored one, and the diagnostics bundle is built from an allowlist rather than by filtering a dump. |
| 🧰 | **Contributor-friendly tooling** | Bun workspaces, Biome, TypeScript 7, Orval, sqlc, Atlas, Mintlify, Renovate, Release Please, and Conventional Commits. |

## 🤖 Telegram experience

The bot is a complete customer product in a single-message interface:

- 🛒 Plan catalog with period, traffic, device, and price comparison, plan details, and policy-aware purchase, renewal, upgrade, and downgrade
- 💳 Telegram Stars invoices, CryptoBot invoices, YooKassa hosted checkout, and audited manual payments — offered only when configured and currency-compatible
- 🏷 Promo-code entry with a specific rejection reason, and wallet credit applied first by default
- ⏳ Pending, succeeded, provisioning, completed, failed, cancelled, expired, and refunded screens with retry-safe refresh
- 🧾 Order history with payment status, receipts, and refund state
- ♻️ Trials with abuse controls, renewal reminders, grace-period explanations, expired-subscription recovery, and honest auto-renew status
- 📊 Subscription status, expiry, remaining days, and traffic progress
- 🚀 Per-platform connection guides with client deep links and a manual-copy fallback
- 📱 Privacy-safe device management with per-device and remove-all confirmation
- 🔐 Subscription-link rotation for compromised credentials
- 💬 Support desk with conversation history, replies, attachments, unread state, and close/reopen
- 📰 News and service-announcement inbox with read state
- 🔔 Classified notifications with explicit marketing consent, quiet hours, and frequency caps
- 🎁 Referral terms, qualified-referral progress, and rewards granted exactly once
- 🌍 Complete Russian and English copy for every success, empty, pending, and failure state

A first-time visitor can browse and buy before any Remnawave user exists. For an existing customer, Omniflow performs an exact Telegram-ID lookup through Remnawave and persists the numeric user mapping under a concurrency-safe database lock. It never guesses account ownership or offers an insecure self-link.

## 🧩 Clear ownership boundaries

```mermaid
flowchart LR
    Customer["Customer"] --> Telegram["Telegram bot"]
    Customer --> Web["Unified web app"]
    Operator["Operator"] --> Admin["/admin workspace"]
    Telegram --> API["Go application"]
    Web --> API
    Admin --> API
    API --> PostgreSQL[("PostgreSQL")]
    API --> Valkey[("Valkey")]
    API --> Remnawave["Remnawave API"]
    Remnawave --> Xray["Xray core"]
```

**Remnawave owns:** VPN users, traffic, devices, access links, nodes, squads, and Xray state.

**Omniflow owns:** customer identity, plans, orders, payments, wallet entries, support, campaigns, referrals, loyalty, RBAC, and audit history.

Omniflow never connects to Remnawave storage and never manages Xray processes directly.

## 🛠️ Technology stack

| Layer | Technology |
| --- | --- |
| Backend | Go 1.26, Chi, pgx, sqlc, River, OpenAPI, OpenTelemetry |
| Data | PostgreSQL 18, Atlas migrations, Valkey 9 |
| Telegram | `go-telegram/bot`, localized inline keyboards, long polling |
| Web | Next.js 16.3, React 19, TypeScript 7, SWR, Zustand |
| UI and forms | Tailwind CSS v4, shadcn/ui, React Day Picker, Recharts, React Hook Form, Zod |
| API generation | `oapi-codegen`, Orval Fetch/SWR clients and Zod schemas |
| Tooling | Bun workspaces, Biome, Mintlify, Docker Compose |
| Delivery | GitHub Actions, Renovate, Release Please, Trivy, Gitleaks, CodeQL, Cosign |

## 🚀 Quick start

### Requirements

- Docker with Compose v2
- A reachable Remnawave 3.2.2 installation
- A Telegram bot token if you want to run bot flows

### 1. Configure the application

```bash
cp .env.example .env
```

Set at least the database, Remnawave, and Telegram values required by the services you want to run. All project-owned environment variables use the `APP_*` prefix.

### 2. Start the backend and bot

```bash
docker compose up --build postgres valkey migrate api bot worker
```

The bot remains disabled when `APP_TELEGRAM_TOKEN` is empty. Check the API at:

```bash
curl http://localhost:8080/healthz
```

### 3. Start the unified web application

A local stack speaks plain HTTP, and a session cookie marked `Secure` is one a
browser will not send back over it. Both panels would render and refuse every
sign-in. Turn the flag off for local use only:

```env
APP_ADMIN_COOKIE_SECURE=false
APP_CUSTOMER_COOKIE_SECURE=false
```

```bash
docker compose --profile web up --build
```

- 👤 Customer account: [http://localhost:3000](http://localhost:3000)
- 🧑‍💻 Admin workspace: [http://localhost:3000/admin](http://localhost:3000/admin)

Both panels call the API on their own origin, and the web service forwards `/v1`
to it. Behind a reverse proxy, route `/v1` on the web host as the examples in
`deploy/proxies` do; see [Reverse proxy](./docs/operations/reverse-proxy.mdx).

On first start the API writes a one-time setup token to its log. Redeem it at
`/admin/setup` to create the first owner; see [Admin panel](./docs/operations/admin-panel.mdx).
- ❤️ API health: [http://localhost:8080/healthz](http://localhost:8080/healthz)

For complete configuration and deployment guidance, see the [quick-start documentation](./docs/getting-started/quickstart.mdx).

## 🗂️ Repository layout

```text
apps/web/             Unified customer and admin Next.js application
cmd/api/              REST API process
cmd/bot/              Telegram bot process
cmd/worker/           River background worker
internal/botapp/      Telegram navigation, rendering, and identity linking
internal/remnawave/   Versioned Remnawave HTTP adapter
packages/api-client/  Generated Fetch, SWR, and Zod bindings
packages/ui/          Shared shadcn component system
database/             Desired schema, Atlas migrations, and sqlc queries
docs/                 Mintlify documentation
deploy/proxies/       Optional reverse-proxy examples
```

## 🔭 Roadmap

The complete versioned delivery contract is maintained in [ROADMAP.md](./ROADMAP.md). The mandatory order is Telegram bot and backend, then the admin panel, then the customer panel.

- [x] Go, PostgreSQL, Valkey, Atlas, River, and OpenAPI foundation
- [x] Remnawave 3.2.2 client boundary and exact Telegram account linking
- [x] Telegram subscription, security, devices, preferences, alerts, referrals, and support UX
- [x] Unified customer/admin Next.js application and shared components
- [x] Plans, orders, payments, refunds, wallet ledger, entitlements, and Remnawave fulfillment backend
- [x] Telegram plan discovery, checkout, renewals, and post-payment lifecycle UX
- [x] Telegram support desk, news inbox, communication consent, and referral reward policies
- [x] Operator authentication, two-factor, optional OIDC, RBAC enforcement, and audit search
- [x] Responsive `/admin` shell with command search, localization, and URL-backed filters
- [x] Operator dashboard, customer, subscription, catalogue, finance, risk, and job workflows
- [x] Customer-facing gift, digital-goods, and auto-renew flows in the bot
- [x] Operator support desk, audience segments, campaigns, referrals, and loyalty
- [x] Installation settings, diagnostics, and telemetry preview without SQL access
- [x] Optional AI features and MCP connections, off until an owner configures them
- [x] End-to-end browser coverage with accessibility, layout, and localization gates
- [x] Customer web panel: sign-in, dashboard, and subscription management
- [x] Customer web checkout, shop, support, and referrals
- [x] 1.0: compatibility matrix, signed release artifacts, reliability runbooks, and the complete public documentation set

Two items remain outstanding for 1.0, and neither is something a change to this
repository produces: an independent security review, and a green CI run on the
release commit. The third — a restore drill against a real database — was closed
by running one: the procedure is `tools/restore-drill.sh`, and it passes against
a live installation.

## 📡 Anonymous telemetry

Privacy-preserving installation telemetry is enabled by default to help prioritize compatibility and features across the open-source community. It never includes customer identities, Telegram IDs, payment values, VPN traffic, domains, tokens, links, or message content.

Disable all telemetry requests with:

```bash
APP_TELEMETRY_ENABLED=false
```

The exact payload, retention policy, and opt-out behavior are documented in [Anonymous telemetry](./docs/operations/telemetry.mdx).

## 🤝 Contributing

Contributions are welcome. Before opening a pull request:

1. Read [CONTRIBUTING.md](./CONTRIBUTING.md) and [AGENTS.md](./AGENTS.md).
2. Review the [architecture](./docs/architecture/overview.mdx), the ownership boundaries, and the [decision records](./docs/architecture/decisions.mdx) that constrain what a change may do.
3. Run the relevant commands from `make help`.
4. Use a focused [Conventional Commit](https://www.conventionalcommits.org/) — the type decides the next version, as described in the [release process](./docs/contributing/releases.mdx).

Building against Omniflow rather than on it? [Extension policy](./docs/integrations/extending.mdx) says which surfaces are public and stable, what you can plug in with configuration alone, and what deliberately cannot be extended.

Questions, bug reports, and feature proposals each have their own route — see [SUPPORT.md](./SUPPORT.md).

Please report security vulnerabilities privately using [SECURITY.md](./SECURITY.md), not public issues.

## 📄 License

Copyright © 2026 Omniflow contributors.

Licensed under the **Apache License, Version 2.0**. You may use, modify, and distribute this project under the terms in [LICENSE](./LICENSE).
