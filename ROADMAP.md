<!-- markdownlint-disable MD013 -->

# 🗺️ Omniflow roadmap

This document is the delivery contract for Omniflow. Versions are ordered by product dependency, not by estimated date:

```text
Telegram Bot + backend → Admin web panel → Customer web panel → 1.0 GA
```

Work may prepare shared foundations early, but a later product surface must not displace unfinished requirements from the current phase. A version is complete only when its listed behavior, migrations, documentation, security controls, and release gates are complete.

## Status legend

- ✅ Released or implemented on `main`
- 🚧 Current development phase
- ⏳ Planned and blocked by an earlier phase
- 🔭 Post-1.0 or optional

## Product boundaries

- Remnawave is authoritative for VPN users, traffic, devices, subscription links, nodes, hosts, squads, and Xray state.
- Omniflow is authoritative for customer identity, catalog, orders, payments, refunds, wallet, fulfillment intent, support, referrals, loyalty, marketing consent, RBAC, and audit history.
- Omniflow uses only the supported Remnawave API. It never writes to Remnawave storage or manages Xray directly.
- PostgreSQL is the only durable application store. Valkey is limited to cache, rate limits, locks, and disposable state.
- The customer and admin panels remain one Next.js application and one shared shadcn component system.
- Telegram carries operator notifications and backup/restore actions only. Every other administrative control lives in the web panel.

---

## ✅ v0.1 — Platform foundation

Goal: establish a safe, contributor-friendly modular monolith.

### Backend and data

- [x] Go modular monolith with independent `api`, `bot`, and `worker` processes
- [x] PostgreSQL 18, pgx, sqlc, Atlas migrations, and migration checksums
- [x] Valkey 9 boundary for ephemeral data
- [x] River durable-job foundation and transactional outbox
- [x] OpenAPI 3.0.3 source contract and generated Go server types
- [x] Structured `slog`, OpenTelemetry, Prometheus, health endpoints, and request IDs
- [x] Optional, default-enabled anonymous installation telemetry with complete opt-out

### Frontend and tooling

- [x] Bun workspaces and committed Bun lockfile
- [x] Next.js 16.3, React 19, and TypeScript 7
- [x] SWR, Zustand, Tailwind CSS v4, shadcn/ui, React Hook Form, and Zod
- [x] Orval-generated Fetch/SWR clients and Zod schemas
- [x] Biome as the only frontend formatter and linter
- [x] One web application containing customer routes and `/admin`

### Delivery

- [x] Docker Compose development and single-server deployment foundation
- [x] Optional Caddy example while allowing Caddy, Traefik, or another reverse proxy
- [x] GitHub Actions, dependency automation, security scans, and Release Please workflow
- [x] Mintlify documentation under `docs/`
- [x] Apache License 2.0, contribution guide, security policy, and agent instructions

---

## ✅ v0.2 — Telegram self-service core

Goal: provide a useful customer bot before commerce is introduced.

### Account and subscription

- [x] Exact Telegram-ID lookup through Remnawave
- [x] Concurrency-safe identity linking without insecure self-linking
- [x] Private-chat-only operation and protected account messages
- [x] Subscription status, expiry, remaining days, and traffic progress
- [x] Protected subscription open/copy actions and connection instructions
- [x] Subscription-link rotation with explicit confirmation

### Devices and safety

- [x] Privacy-safe device names and last-seen dates
- [x] Per-device removal and remove-all operations
- [x] Confirmation and recoverable error states for destructive actions
- [x] No HWID, IP address, token, or subscription secret in displayed text, callback data, or logs

### Experience

- [x] Russian and English interfaces with automatic locale selection
- [x] Persisted language override and notification preferences
- [x] Single-message inline navigation, loading, empty, retry, and back states
- [x] `/start`, `/menu`, `/settings`, `/support`, and `/cancel`
- [x] Persisted support request capture
- [x] Referral deep links, immutable attribution, and invited-user count
- [x] Idempotent expiry alerts at 7, 3, 1, and 0 days
- [x] Idempotent traffic alerts at 80% and 100%

---

## ✅ v0.3 — Commerce and customer-domain backend

Goal: build the complete financial and entitlement model before exposing purchases.

### Customer and identity

- [x] Canonical customer profile independent of Telegram and Remnawave identifiers
- [x] Verified identity methods and safe account-link/unlink lifecycle
- [x] Contact-channel preferences, locale, timezone, and consent records
- [x] Customer suspension, deletion, anonymization, and retention workflows
- [x] Conflict-safe import of existing Remnawave customers
- [x] Import preview, validation report, resumability, and rollback-safe failure handling

### Catalog and pricing

- [x] Plans with stable codes, localized names/descriptions, visibility, and sort order
- [x] Plan versions so historical orders never change when catalog pricing changes
- [x] Billing periods, duration, traffic allowance, device limit, and assigned Remnawave squads
- [x] Integer minor-unit prices and explicit ISO currency
- [x] Trials, one-time plans, recurring-capable plans, and free/manual plans
- [x] Promotions with validity windows, redemption limits, customer eligibility, and plan scope
- [x] Promo codes with normalized lookup, brute-force rate limiting, and atomic redemption
- [x] Upgrade, downgrade, extension, and cancellation policies without implicit proration

### Orders, payments, and refunds

- [x] Draft, pending, paid, fulfilled, cancelled, expired, partially refunded, and refunded order states
- [x] Idempotency keys for every order and payment mutation
- [x] Provider-neutral payment intent and provider capability contract
- [x] Verified, replay-safe webhook intake with raw-event retention and deduplication
- [x] Payment status polling/reconciliation when a provider webhook is late or missing
- [x] Full and partial refund records without rewriting payment history
- [x] Receipt/fiscalization metadata boundary where required by a provider
- [x] Currency mismatch, duplicate payment, late payment, overpayment, and underpayment handling
- [x] Telegram Stars adapter boundary (authenticated Bot API settlement completes in v0.4)
- [x] CryptoBot adapter
- [x] YooKassa adapter
- [x] Manual/offline payment workflow with operator approval and audit trail

### Wallet and ledger

- [x] Append-only double-entry-style customer ledger using integer minor units
- [x] Credit, debit, payment, refund, referral reward, correction, and expiration entry types
- [x] Deterministic balance calculation and per-currency isolation
- [x] Wallet-first payment application with an explicit remaining external amount
- [x] Idempotent ledger references and compensating entries instead of updates/deletes
- [x] Operator adjustments requiring reason, permission, and audit event

### Entitlements and Remnawave fulfillment

- [x] Entitlement records separated from payment and Remnawave observed state
- [x] Idempotent create, extend, enable, disable, reset-traffic, limit, and squad operations
- [x] Paid order commits a durable fulfillment job in the same database transaction
- [x] Retry with backoff when Remnawave is unavailable without losing successful payment state
- [x] Scheduled drift reconciliation and operator-visible mismatch reasons
- [x] Expiry, traffic, device-limit, and status synchronization
- [x] Safe handling of externally edited or deleted Remnawave users
- [x] Fulfillment history with request correlation and no secret payload storage

### v0.3 release gates

- [x] Failure-path and idempotency tests for orders, webhooks, wallet, and fulfillment
- [x] Atlas migration review and sqlc regeneration
- [x] OpenAPI coverage for all domain operations
- [x] No checkout UI enabled before at least one real provider passes sandbox integration tests
- [x] Financial invariants documented and tested

---

## ✅ v0.4 — Complete Telegram commerce and lifecycle

Goal: make the bot a complete customer product using the v0.3 backend.

### Discovery and purchase

- [x] Localized plan catalog with clear period, traffic, device, and price comparison
- [x] Plan details, eligibility, promotion, and terms confirmation
- [x] New subscription purchase and existing subscription renewal
- [x] Upgrade/downgrade choices that reflect the configured plan policy
- [x] Promo-code entry, validation, rejection reason, and removal
- [x] Wallet balance display and wallet-credit application
- [x] Provider selection based only on enabled and compatible adapters
- [x] Telegram Stars invoice flow
- [x] CryptoBot payment flow
- [x] YooKassa hosted checkout flow
- [x] Pending-payment screen with refresh and expiry
- [x] Success, failure, cancellation, timeout, duplicate, and delayed-webhook states
- [x] Order history, payment status, receipt link, and refund status

### Subscription lifecycle

- [x] Trial activation with abuse controls
- [x] Renewal reminders with direct, idempotent checkout actions
- [x] Expired-subscription recovery
- [x] Grace-period and limited-state explanations
- [x] Auto-renew status and cancellation when supported by the selected provider
- [x] Clear post-payment provisioning progress and retry-safe status refresh
- [x] Connection instructions by platform and supported client
- [x] App deep links with manual-copy fallback

### Support, referrals, and communication

- [x] Support ticket list, status, conversation history, replies, and close/reopen actions
- [x] Operator reply delivery with deduplication and unread state
- [x] Attachment support with size/type restrictions and retention policy
- [x] Referral terms, reward progress, qualified referral count, and ledger history
- [x] Configurable inviter/invitee rewards granted exactly once after qualification
- [x] News and service-announcement inbox
- [x] Transactional versus marketing message classification
- [x] Explicit marketing consent, unsubscribe, quiet hours, and frequency caps
- [x] Maintenance, incident, payment, fulfillment, renewal, and support notifications

### Abuse and reliability

- [x] Per-user and per-action Valkey rate limits
- [x] Callback replay protection for payment and destructive actions
- [x] Telegram API retry, flood-wait handling, and delivery failure classification
- [x] Bot-blocked/user-deactivated handling without endless retries
- [x] Correlation IDs across Telegram update, order, payment, job, and Remnawave request
- [x] Complete Russian and English copy for every success, empty, pending, and failure state

---

## 🚧 v0.5 — Purchase expansion and production release

Goal: complete the customer purchase model, then declare the Telegram-first product production-ready before admin UI development becomes the primary focus.

Every capability in this version is configured through environment and configuration files until the admin panel exposes it in v0.7 and v0.8. No feature below may depend on an admin UI that does not exist yet.

### Wallet and balance top-up

- [ ] Customer-initiated top-up with operator-configured preset amounts and free entry
- [ ] Top-up through any enabled provider reusing the existing order, webhook, and reconciliation pipeline
- [ ] Top-up credited as an idempotent ledger entry with per-currency isolation
- [ ] Minimum, maximum, and rolling-window top-up limits with explicit rejection reasons
- [ ] Top-up history with pending, failed, expired, and duplicate-payment recovery
- [ ] Overpayment and underpayment resolved into the ledger rather than the order

### Cart and deferred purchase

- [ ] Persistent cart surviving insufficient balance, session loss, and navigation
- [ ] Cart holds plan, period, selected squads, add-ons, and applied promo code
- [ ] Price and eligibility re-validated against the current plan version before any charge
- [ ] Automatic purchase of the saved cart once a top-up covers the outstanding amount
- [ ] Idempotent auto-purchase that cannot double-charge on duplicate or replayed credits
- [ ] Cart expiry, manual clearing, and explicit cancellation of pending auto-purchase

### Recurring payments

- [ ] Per-provider recurring capability declared through the provider contract
- [ ] Per-merchant override for providers that do not grant card binding to every merchant
- [ ] Saved payment method referenced only by provider token, with no card data stored by Omniflow
- [ ] Customer-visible saved methods, default selection, and removal
- [ ] Auto-renew from a saved method or wallet balance with a configurable lead time
- [ ] Failed-charge retry schedule, dunning notification, and automatic fallback to manual renewal
- [ ] Auto-renew disabled by default and enabled only after explicit customer consent

### Subscription configurator and add-ons

- [ ] Plan-scoped squad sets so different plans expose different Remnawave squads
- [ ] Automatic squad assignment or customer selection according to the plan policy
- [ ] Mid-period add-on purchase of traffic, device slots, and additional squads
- [ ] Add-on prices versioned with the plan so historical orders never change
- [ ] Add-on entitlement applied idempotently through the existing fulfillment pipeline
- [ ] Explicit, documented proration rules for every add-on instead of implicit behavior

### Gifts

- [ ] Purchase a subscription, add-on, or wallet credit for another person
- [ ] Gift codes claimable by an unlinked recipient with claim, expiry, and revocation states
- [ ] Telegram gift delivery with an optional sender message and privacy-safe recipient handling
- [ ] Gift orders kept separate from the recipient's own order and payment history
- [ ] Refund, abuse, and reclaim rules for unclaimed, expired, and disputed gifts

### Promotions and personal offers

- [ ] Promo-code reward types for fixed amount, percentage, subscription days, and trial grant
- [ ] Wallet-credit promo codes recorded as ordinary ledger entries
- [ ] Stacking rules, precedence order, and explicit rejection reasons
- [ ] Personal offers targeted at a single customer with validity window and single-use redemption
- [ ] Offer presentation in the bot with expiry countdown, terms, and dismissal

### Mandatory channel subscription

- [ ] Operator-configured required Telegram channels with per-channel enablement
- [ ] Membership verification before purchase and before subscription activation
- [ ] Periodic re-verification with automatic entitlement disable on unsubscribe
- [ ] Grace period, warning notification, and automatic restore on rejoin
- [ ] Exemption list, per-customer bypass, and a complete audit trail for every state change

### Runtime and operations

- [ ] Secret-token-validated Telegram webhook mode
- [ ] Long polling retained as an explicit development/fallback mode
- [ ] Provider webhook endpoints with signature verification and body-size limits
- [ ] `/livez`, `/readyz`, dependency health, and graceful shutdown
- [ ] Prometheus metrics for API, bot, jobs, webhooks, payments, and Remnawave calls
- [ ] OpenTelemetry traces across HTTP, Telegram, River, PostgreSQL, and providers
- [ ] Structured redaction tests for tokens, links, payment payloads, and customer content
- [ ] Job retry/dead-letter visibility through operational APIs
- [ ] PostgreSQL backup, restore, upgrade, and rollback documentation
- [ ] Data-retention and cleanup jobs for sessions, provider payloads, attachments, and telemetry

### Operator notifications and backups in Telegram

- [ ] Operator supplies a group ID and the bot creates and binds every required forum topic itself
- [ ] Purchase, renewal, top-up, refund, fulfillment-failure, and incident events routed to their own topic
- [ ] Topic rebinding, recreation after deletion, and clear failure states when permissions are missing
- [ ] Notification volume controls so a burst cannot flood an operator group
- [ ] Scheduled PostgreSQL backups with retention, encryption, and integrity verification
- [ ] Backup status and restore initiated from the bot with confirmation, permission check, and audit event
- [ ] No customer content, secret, or payment payload in any operator notification

### Maintenance mode and anomaly monitoring

- [ ] Maintenance mode with manual activation and automatic detection of Remnawave or panel unavailability
- [ ] Maintenance mode blocks purchases and fulfillment while preserving already-paid state
- [ ] Localized customer notice, expected-return messaging, and automatic exit on recovery
- [ ] Traffic, purchase, refund, and referral anomaly detection with configurable thresholds
- [ ] Anomaly alerts delivered to the operator topic with evidence and no automatic customer punishment

### Quality and compatibility

- [ ] Testcontainers coverage for PostgreSQL, migrations, repositories, outbox, and River jobs
- [ ] Contract tests against supported Remnawave 3.2.x behavior
- [ ] Provider sandbox integration suites and replay fixtures
- [ ] Load tests for update bursts, campaigns, webhook retries, and reconciliation
- [ ] Upgrade test from every supported Omniflow migration baseline
- [ ] Docker images pinned by version with SBOM, provenance, and vulnerability scan
- [ ] Compose deployment tested with Caddy and Traefik examples
- [ ] Complete operator setup, troubleshooting, and disaster-recovery docs

### Definition of bot/backend complete

- [ ] A new operator can install Omniflow, import users, configure a plan and provider, and accept a real sandbox payment from Telegram
- [ ] A successful payment is never lost when Remnawave is unavailable
- [ ] Duplicate Telegram updates, callbacks, webhooks, and jobs cannot duplicate money or entitlement
- [ ] Customers can purchase, connect, renew, manage devices, get support, and review history without a web panel
- [ ] A customer can fund a wallet, keep a cart, and have it purchased automatically once the balance covers it
- [ ] Recurring payment activates only where the provider and the specific merchant genuinely support card binding
- [ ] An operator receives purchase, renewal, top-up, and failure notifications and can restore a backup without a web panel
- [ ] CI, security scanning, documentation validation, and release automation are green

---

## ⏳ v0.6 — Admin panel foundation and access control

Goal: build the secure operator shell only after the Telegram/backend product is complete.

### Authentication and sessions

- [ ] Secure first-owner bootstrap with one-time setup token
- [ ] Password hashing with current recommended parameters
- [ ] TOTP two-factor authentication and recovery codes
- [ ] Session rotation, inactivity timeout, absolute expiry, logout-all, and device/session list
- [ ] Login rate limiting, lockout/backoff, and security notifications
- [ ] CSRF protection, secure cookies, trusted proxy handling, and restrictive security headers
- [ ] Password reset flow that does not disclose account existence
- [ ] Optional OIDC configuration without making an external identity provider mandatory

### RBAC and audit

- [ ] Owner, administrator, support, finance, marketing, and read-only auditor roles
- [ ] Granular permissions enforced in the Go API and Next.js server boundary
- [ ] No authorization decisions based only on hidden routes or frontend state
- [ ] Append-only audit events for authentication, configuration, customer, financial, and support actions
- [ ] Actor, target, action, reason, timestamp, request ID, and safe before/after metadata
- [ ] Audit search, filters, pagination, and export without secrets

### Shared admin application shell

- [ ] Responsive `/admin` layout using shared shadcn primitives
- [ ] Accessible navigation, command search, breadcrumbs, and keyboard operation
- [ ] Russian and English `next-intl` catalogs
- [ ] Typed Orval API hooks, SWR cache policy, and standardized mutations
- [ ] React Hook Form and Zod validation for all settings and mutations
- [ ] Explicit skeleton, empty, partial, stale, permission-denied, and error states
- [ ] Global notifications, confirmation dialogs, destructive-action safeguards, and unsaved-change protection
- [ ] URL-backed filters, cursor pagination, sortable tables, and saved operator preferences

---

## ⏳ v0.7 — Admin operations and commerce

Goal: let operators run day-to-day customer, subscription, and financial operations.

### Overview and system health

- [ ] Dashboard for active/expired customers, traffic, renewals, payment health, open support, and failed jobs
- [ ] Metrics with explicit definitions, timezone, comparison period, and data freshness
- [ ] Remnawave, PostgreSQL, Valkey, Telegram, worker, and provider health
- [ ] Recent incidents, reconciliation drift, webhook failures, and required actions
- [ ] Anomaly review with threshold configuration, supporting evidence, acknowledgement, and dismissal
- [ ] Maintenance-mode state, activation reason, and manual override

### Customers and subscriptions

- [ ] Customer search by safe identifiers with status and segment filters
- [ ] Customer profile with identities, subscription, devices, orders, wallet, referrals, support, consent, and audit timeline
- [ ] Create/link/import customer with duplicate detection
- [ ] Suspend, reactivate, anonymize, and delete according to retention rules
- [ ] Create, extend, enable, disable, reset traffic, change limits, and change squads through Remnawave API
- [ ] Device review and removal without exposing identifiers unnecessarily
- [ ] Bulk import/export with preview, validation errors, progress, resumability, and audit history
- [ ] Bulk actions with permission checks, impact preview, limits, and per-item results
- [ ] External blocklist source configuration, refresh schedule, and connection health
- [ ] Blocklist match review with evidence, manual allowlist override, and appeal handling
- [ ] Block reason, actor, and source recorded as an audit event for every decision

### Catalog and promotions

- [ ] Plan list, create, version, archive, visibility, localization, and ordering
- [ ] Price, currency, duration, traffic, device, squad, and eligibility configuration
- [ ] Trial, upgrade, downgrade, grace-period, and renewal policies
- [ ] Promotion and promo-code management with usage analytics
- [ ] Plan-scoped squad sets, selection policy, and subscription-configurator visibility
- [ ] Add-on catalog for traffic, device slots, and squads with versioned pricing and proration rules
- [ ] Promo-code reward types, stacking rules, and personal-offer targeting with audience preview
- [ ] Preview of customer-facing Telegram and web presentation

### Finance

- [ ] Order and payment search, details, timelines, and provider references
- [ ] Pending/stuck payment reconciliation and safe retry tools
- [ ] Refund workflow with provider capability checks, reason, confirmation, and audit
- [ ] Append-only wallet ledger and permission-gated corrective entries
- [ ] Provider configuration with encrypted secrets, connection test, and webhook status
- [ ] Wallet top-up configuration, limits, preset amounts, and enabled providers
- [ ] Recurring-payment enablement per provider and per merchant with an explicit capability test
- [ ] Saved-payment-method visibility, dunning schedule, and auto-renew failure review
- [ ] Gift order, claim, expiry, revocation, and refund management
- [ ] Financial CSV export with stable schema and timezone/currency clarity
- [ ] Revenue views separated from payment volume, wallet credits, and refunds

### Fulfillment and jobs

- [ ] Fulfillment history and Remnawave drift view
- [ ] Retry/cancel controls constrained by job state and idempotency rules
- [ ] Dead-letter queue view with safe error details
- [ ] Webhook event list, verification status, attempts, and replay-safe reprocessing
- [ ] Outbox lag and unpublished-event diagnostics

---

## ⏳ v0.8 — Complete admin panel

Goal: finish support, communication, configuration, and operational readiness before customer web development.

### Support desk

- [ ] Ticket queues, assignment, priority, tags, status, SLA timestamps, and unread counts
- [ ] Conversation view with safe attachment handling and operator replies delivered to Telegram/web
- [ ] Internal notes distinct from customer-visible messages
- [ ] Canned responses with localization and permission controls
- [ ] Merge/duplicate handling, close/reopen, and complete audit history
- [ ] Support workload and response-time reporting with documented definitions

### Referrals and loyalty

- [ ] Referral program enablement, qualification rule, inviter/invitee reward, cap, and validity period
- [ ] Attribution, qualification, rejected/fraud state, and reward history
- [ ] Manual review and correction through compensating ledger entries
- [ ] Loyalty tiers/rules with versioned definitions and deterministic evaluation
- [ ] Abuse signals and rate limits without opaque automatic account punishment

### News, campaigns, and communication

- [ ] Localized news posts with draft, preview, schedule, publish, unpublish, and archive
- [ ] Audience segments using explicit, reviewable filters
- [ ] Campaign preview, estimated audience, test delivery, schedule, pause, cancel, and results
- [ ] Transactional and marketing templates with variables validated before send
- [ ] Consent, suppression list, quiet hours, frequency caps, and delivery deduplication
- [ ] Delivery states for queued, sent, failed, blocked, and clicked where supported
- [ ] No storage or telemetry of message content outside documented product data

### AI-assisted support

- [ ] Provider-neutral AI gateway with OpenAI-compatible, Anthropic, and Gemini adapters
- [ ] OpenAI-compatible adapter supports operator-selected hosted or local models
- [ ] Separate model, temperature, token limit, timeout, and budget configuration per task
- [ ] Ticket-thread summary with customer intent, actions already attempted, and unresolved questions
- [ ] Suggested ticket reply grounded in the visible conversation and approved knowledge sources
- [ ] Rewrite controls for shorter, clearer, friendlier, more formal, or more technical replies
- [ ] Russian/English translation that preserves product names, links, and template variables
- [ ] Suggested category, priority, tags, sentiment, and escalation reason
- [ ] Similar-ticket and relevant documentation suggestions with source citations
- [ ] Secret, token, subscription-link, payment-data, and unnecessary-PII redaction before model requests
- [ ] Generated replies remain editable drafts; an authorized operator must explicitly send them
- [ ] Clear AI-generated label, provider/model disclosure, retry, cancellation, and fallback states

### Scam, abuse, and social-engineering analysis

- [ ] Explainable risk analysis for scam, phishing, impersonation, social engineering, payment fraud, and referral abuse
- [ ] Evidence list, confidence, uncertainty, and matched policy signals instead of an unexplained score
- [ ] Cross-ticket pattern detection using minimized, permission-safe structured signals
- [ ] Suspicious-link and attachment metadata checks through explicitly configured tools
- [ ] Operator feedback for false positive, confirmed abuse, and insufficient evidence
- [ ] Human review required before suspension, refund denial, wallet correction, or other adverse action
- [ ] AI output can recommend an action but cannot silently punish a customer or mutate financial state
- [ ] Risk models, prompts, thresholds, and policy versions recorded with each assessment

### AI writing and marketing tools

- [ ] Draft news posts, service announcements, campaign messages, subjects, and calls to action
- [ ] Rewrite, shorten, expand, simplify, change tone, and produce operator-requested variants
- [ ] Translate and localize between Russian and English rather than performing literal translation only
- [ ] Preserve and validate template variables, Markdown/HTML rules, Telegram limits, and channel constraints
- [ ] Brand-voice, forbidden-claims, consent, quiet-hours, and communication-policy checks
- [ ] Readability, ambiguity, spam-likelihood, and potentially misleading language review
- [ ] Audience-aware suggestions using aggregate segment definitions without exposing raw customer lists
- [ ] Side-by-side diff, undo, version history, and explicit operator acceptance of generated edits
- [ ] AI may prepare a campaign draft but cannot select recipients, schedule, or send without confirmation

### Admin copilot

- [ ] Permission-aware assistant for explaining dashboards, failed jobs, webhook errors, and reconciliation drift
- [ ] Natural-language search over customers, orders, tickets, and audit history using authorized structured tools
- [ ] Answers cite the records, metrics, documentation, or tool results used to produce them
- [ ] Suggested next actions deep-link to the normal admin workflow rather than bypassing it
- [ ] Read-only by default; every mutation requires a preview, permission check, reason, and confirmation
- [ ] No autonomous payment, refund, wallet, entitlement, suspension, campaign, or role mutation

### MCP integration

- [ ] MCP client with remote Streamable HTTP transport and standards-based authorization
- [ ] Discover MCP tools, resources, and prompts with cached capability metadata and health status
- [ ] Owner-managed MCP server registry with explicit enablement, allowlists, timeouts, and egress restrictions
- [ ] Encrypted MCP credentials that are write-only in the admin interface
- [ ] Per-server and per-tool mapping to Omniflow RBAC permissions
- [ ] JSON Schema validation for every tool input and output before it reaches AI or application code
- [ ] Tool-call preview showing server, tool, arguments, affected records, and expected side effects
- [ ] Human confirmation for external writes and every consequential Omniflow mutation
- [ ] First-party Omniflow MCP server for permission-scoped admin tools, resources, and operational documentation
- [ ] First-party MCP server is read-only by default; mutation capabilities are separately enabled and audited
- [ ] Prompt-injection defenses that treat tickets, webpages, attachments, MCP resources, and tool output as untrusted data
- [ ] Tool recursion, call-count, duration, response-size, and monetary-cost limits
- [ ] Circuit breakers and graceful degradation when an MCP server is unavailable or returns invalid data
- [ ] Full audit trail for connection changes, model requests, tool calls, confirmations, results, and failures

### AI privacy, governance, and cost controls

- [ ] AI and MCP are globally optional and disabled until an owner configures them
- [ ] Per-feature enablement for support, marketing, scam analysis, copilot, and MCP tools
- [ ] Data-use preview shows operators exactly which fields will leave the installation
- [ ] Provider-specific retention/training warnings and documented zero-retention options where available
- [ ] Configurable prompt/output retention with deletion and legal-hold behavior
- [ ] AI prompts, outputs, ticket content, customer data, and tool arguments never enter anonymous telemetry
- [ ] Per-role, per-operator, per-feature, and installation-wide usage limits
- [ ] Token, request, latency, error, and estimated-cost reporting without exposing prompt content in metrics
- [ ] Provider/model fallback is explicit and cannot route data to an unapproved provider
- [ ] Audit exports identify generated content and consequential decisions influenced by AI

### AI and MCP quality gates

- [ ] Sanitized evaluation sets for support replies, translations, marketing edits, and scam analysis
- [ ] Regression thresholds for correctness, groundedness, citation validity, tone, and unsafe advice
- [ ] Prompt-injection, tool-confusion, data-exfiltration, privilege-escalation, and indirect-injection tests
- [ ] Tests proving operators cannot invoke tools beyond their own RBAC permissions through AI or MCP
- [ ] Tests proving duplicate or retried tool calls cannot duplicate financial or provisioning effects
- [ ] Model/provider outage, timeout, malformed output, budget exhaustion, and partial-tool-failure tests
- [ ] AI features degrade to normal manual admin workflows without blocking support or operations

### Settings and operations

- [ ] General branding, service name, support contacts, locale, timezone, and public URLs
- [ ] Remnawave connection, compatibility check, reconciliation schedule, and safe token rotation
- [ ] Telegram bot identity, webhook, command, and delivery status
- [ ] Operator group and topic binding with automatic topic creation and permission diagnostics
- [ ] Mandatory channel list, verification schedule, grace period, and exemption management
- [ ] Maintenance-mode policy, dependency detection thresholds, and customer notice text
- [ ] Payment-provider configuration and capability matrix
- [ ] Notification thresholds, templates, and test delivery
- [ ] AI providers, model routing, budgets, privacy controls, evaluation status, and connection tests
- [ ] MCP server registry, authorization, capability allowlists, health, and audit history
- [ ] Telemetry status, exact payload preview, and global opt-out
- [ ] Operator, role, session, and security settings
- [ ] Backup schedule, retention, encryption, restore history, and verified test restore
- [ ] Backup status, version, migration status, update availability, and diagnostics bundle
- [ ] Secrets never returned after write and always excluded from diagnostics

### Definition of admin complete

- [ ] An owner can configure and operate every backend capability without SQL or shell access
- [ ] Support and finance roles can do their jobs without receiving unrelated permissions
- [ ] Every sensitive mutation is authorized, validated, confirmed where necessary, and audited
- [ ] AI-assisted work is grounded, editable, attributable, and never required for a manual workflow
- [ ] MCP tools cannot bypass RBAC, mutation confirmation, idempotency, or audit requirements
- [ ] Testcontainers cover repositories and critical workflows
- [ ] Playwright covers admin authentication and highest-risk operator journeys
- [ ] Accessibility, responsive layout, localization, and browser support gates pass

---

## ⏳ v0.9 — Customer web foundation

Goal: expose the proven customer capabilities through the shared web application.

### Authentication and account security

- [ ] Telegram-based sign-in linked to the same canonical customer identity
- [ ] Time-limited magic-link fallback where enabled by the operator
- [ ] Session rotation, logout-all, inactivity/absolute expiry, CSRF protection, and secure cookies
- [ ] Account-link conflict handling without revealing another customer’s existence
- [ ] Customer-visible active sessions and security events
- [ ] Suspended, deleted, and unlinked account states

### Shared customer shell

- [ ] Responsive mobile-first layout sharing the admin component system
- [ ] Russian and English localization with persisted preference
- [ ] Accessible navigation, keyboard behavior, focus handling, and reduced motion
- [ ] Typed SWR data loading and Zustand only for complex local workflows
- [ ] Explicit loading, empty, stale, offline, partial, and error states
- [ ] Secure handling of subscription links with no analytics or accidental preview leakage

### Dashboard and subscription

- [ ] Status, expiry, remaining days, traffic, device usage, and active plan
- [ ] Traffic visualization with accessible textual equivalent
- [ ] Subscription open/copy, QR/deep-link connection, and platform instructions
- [ ] Subscription-link rotation with reauthentication/confirmation
- [ ] Device list, per-device removal, and remove-all confirmation
- [ ] Renewal, expiry, grace-period, and incident notices

---

## ⏳ v0.10 — Complete customer web panel

Goal: reach feature parity with the customer-facing bot while taking advantage of web interaction patterns.

### Plans and checkout

- [ ] Plan comparison with localized terms and transparent price/currency/period
- [ ] Trial, purchase, renewal, upgrade, and downgrade flows
- [ ] Promo-code entry and eligibility explanations
- [ ] Wallet balance and exact application breakdown
- [ ] Enabled payment-provider selection and hosted/embedded provider handoff
- [ ] Pending, successful, failed, cancelled, duplicate, and delayed-payment recovery
- [ ] Provisioning progress that survives refresh and duplicate submissions
- [ ] Order, payment, receipt, wallet, and refund history

### Support and communication

- [ ] Support ticket list, create, conversation, attachment, reply, close, and reopen
- [ ] Read/unread state synchronized with Telegram where possible
- [ ] News and service-announcement inbox
- [ ] Notification preferences, marketing consent, and unsubscribe controls
- [ ] Browser notifications only after explicit customer permission

### Referrals and account

- [ ] Referral link/code, share action, terms, invited/qualified counts, and reward history
- [ ] Loyalty status and progress when enabled
- [ ] Profile, locale, timezone, contact channel, and privacy settings
- [ ] Personal-data export request and account deletion workflow
- [ ] Clear handoff to support for identity conflicts and irreversible actions

### Customer-web release gates

- [ ] Playwright coverage for sign-in, subscription, checkout, device security, referral, and support journeys
- [ ] Cross-surface contract tests proving Telegram and web produce the same domain outcomes
- [ ] WCAG 2.2 AA review of core journeys
- [ ] Responsive testing for supported mobile, tablet, and desktop widths
- [ ] No duplicate business rules in React; API remains authoritative
- [ ] Performance budgets for JavaScript, images, server response, and core web vitals

---

## ⏳ v1.0 — General availability

Goal: publish a stable release suitable for public single-server production use.

### Compatibility and upgrades

- [ ] Published compatibility matrix for Omniflow, Remnawave, PostgreSQL, Valkey, Go, Bun, and browsers
- [ ] Semantic versioning, changelog, signed release artifacts, container images, SBOM, and provenance
- [ ] Automated upgrade tests and documented backup/restore/rollback procedure
- [ ] Migration policy and supported upgrade window
- [ ] Deprecation policy for API, environment variables, database behavior, and integrations

### Security and privacy

- [ ] Threat model covering identity, Telegram, payments, webhooks, admin RBAC, SSRF, AI, MCP, prompt injection, and supply chain
- [ ] Independent security review of authentication, authorization, payments, and secret handling
- [ ] Dependency, secret, SAST, container, and license scans enforced in release CI
- [ ] Rate limits and abuse controls verified under load
- [ ] Public privacy documentation, retention defaults, telemetry contract, and complete opt-out verification
- [ ] AI/MCP data-flow inventory, provider disclosures, retention controls, and permission review
- [ ] Security reporting and supported-version policy

### Reliability and operations

- [ ] Defined service-level indicators for API, bot, jobs, payments, fulfillment, and notifications
- [ ] Dashboards and alerts for actionable failure modes
- [ ] Backup restoration drill and disaster-recovery runbook
- [ ] Bounded retries, dead-letter handling, and reconciliation for every external side effect
- [ ] Graceful degradation when Valkey, Remnawave, Telegram, or a payment provider is unavailable
- [ ] Capacity guidance for small, medium, and large single-server installations

### Documentation and community

- [ ] End-to-end installation, configuration, migration, upgrade, backup, and troubleshooting guides
- [ ] Bot customer guide, admin operator guide, and customer web guide
- [ ] Integration guides for Remnawave, Telegram, and every supported payment provider
- [ ] AI-provider, local-model, MCP client/server, privacy, cost-control, and troubleshooting guides
- [ ] Public API reference and extension policy
- [ ] Contributor setup, architecture decisions, testing strategy, and release process
- [ ] Issue templates, feature-request process, support boundaries, and code of conduct

### Definition of 1.0

- [ ] A clean installation can be completed from public documentation alone
- [ ] Telegram bot, admin panel, and customer panel cover their complete required journeys
- [ ] Real payments and Remnawave fulfillment are idempotent and recoverable
- [ ] Roles prevent unauthorized operator access and every sensitive operation is audited
- [ ] Backup restore and supported-version upgrade have been exercised successfully
- [ ] CI, security, migration, documentation, accessibility, and end-to-end gates are green

---

## 🔭 Post-1.0 candidates

These items require demonstrated demand and must not delay the core roadmap:

- Kubernetes, Helm charts, and Pulumi deployment modules
- Multi-instance control plane and fleet management
- NATS JetStream event distribution for scale beyond the modular monolith
- WASM extension/plugin runtime with explicit capability sandboxing
- Additional payment providers added when operator demand appears rather than for provider-count parity
- Regional fiscalization adapters, including self-employed receipt reporting for the Russian market
- Guest checkout and configurable sales landing pages that sell without authorization
- Referral balance withdrawal and payout processing
- Contests, daily games, and prize mechanics
- Multi-level partner/affiliate program with network reporting, scheduled after every other post-1.0 candidate
- Additional languages beyond Russian and English
- Native mobile applications
- Advanced fraud scoring, experimentation, and product analytics
- Public marketplace for themes, notification templates, and integrations
- Autonomous multi-step operations only after a separate safety and authorization design review

## Explicit non-goals before 1.0

- Managing Xray processes or writing directly to Remnawave storage
- Requiring Kubernetes, a message broker, or a plugin runtime
- Maintaining separate customer and admin frontend projects
- Custom marketing landing pages with built-in analytics
- Administering the bot from Telegram beyond operator notifications and backup/restore actions
- Bundling or requiring a specific reverse proxy
- Replacing PostgreSQL durability with Valkey
- Shipping placeholder payment screens without a working provider
- Collecting customer or business data in anonymous project telemetry
- Allowing AI or MCP to bypass RBAC, confirmations, idempotency, audit history, or human accountability
