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
- Digital goods that are not VPN access are fulfilled by an external goods provider through its own adapter. They never create, modify, or depend on a Remnawave entitlement.
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

## ✅ v0.5 — Purchase expansion and production release

Goal: complete the customer purchase model, then declare the Telegram-first product production-ready before admin UI development becomes the primary focus.

Every capability in this version is configured through environment and configuration files until the admin panel exposes it in v0.7 and v0.8. No feature below may depend on an admin UI that does not exist yet.

### Wallet and balance top-up

- [x] Customer-initiated top-up with operator-configured preset amounts and free entry
- [x] Top-up through any enabled provider reusing the existing order, webhook, and reconciliation pipeline
- [x] Top-up credited as an idempotent ledger entry with per-currency isolation
- [x] Minimum, maximum, and rolling-window top-up limits with explicit rejection reasons
- [x] Top-up history with pending, failed, expired, and duplicate-payment recovery
- [x] Overpayment and underpayment resolved into the ledger rather than the order

### Cart and deferred purchase

- [x] Persistent cart surviving insufficient balance, session loss, and navigation
- [x] Cart holds plan, period, selected squads, add-ons, and applied promo code
- [x] Price and eligibility re-validated against the current plan version before any charge
- [x] Automatic purchase of the saved cart once a top-up covers the outstanding amount
- [x] Idempotent auto-purchase that cannot double-charge on duplicate or replayed credits
- [x] Cart expiry, manual clearing, and explicit cancellation of pending auto-purchase

### Subscription configurator and add-ons

- [x] Plan-scoped squad sets so different plans expose different Remnawave squads
- [x] Automatic squad assignment or customer selection according to the plan policy
- [x] Mid-period add-on purchase of traffic, device slots, and additional squads
- [x] Add-on prices versioned with the plan so historical orders never change
- [x] Add-on entitlement applied idempotently through the existing fulfillment pipeline
- [x] Explicit, documented proration rules for every add-on instead of implicit behavior

### Multiple concurrent subscriptions (optional)

- [x] Operator switch allowing more than one active subscription per customer, disabled by default
- [x] One customer mapped to several Remnawave users with a stable, customer-visible label per subscription
- [x] Plan, period, squads, device limit, traffic, and expiry tracked independently per subscription
- [x] Configurable maximum concurrent subscriptions per customer and per plan
- [x] Explicit subscription targeting in every purchase, renewal, extension, upgrade, downgrade, and cancellation flow
- [x] Per-subscription device management, link rotation, and connection instructions
- [x] Expiry and traffic alerts keyed per subscription so one subscription cannot suppress another's notification
- [x] Bot copy that names the affected subscription unambiguously in every state and notification
- [x] Wallet, promo eligibility, referral, and trial rules evaluated per customer, never per subscription
- [x] Trial abuse controls that count customers rather than subscriptions
- [x] Fulfillment, reconciliation, and drift detection scoped to the correct Remnawave user
- [x] Single-subscription installations keep the current one-screen experience with no extra selection step
- [x] Documented migration path in both directions, including safe return to single-subscription mode

### Runtime and operations

- [x] Secret-token-validated Telegram webhook mode
- [x] Long polling retained as an explicit development/fallback mode
- [x] Provider webhook endpoints with signature verification and body-size limits
- [x] `/livez`, `/readyz`, dependency health, and graceful shutdown
- [x] Prometheus metrics for API, bot, jobs, webhooks, payments, and Remnawave calls
- [x] OpenTelemetry traces across HTTP, Telegram, River, PostgreSQL, and providers
- [x] Structured redaction tests for tokens, links, payment payloads, and customer content
- [x] Job retry/dead-letter visibility through operational APIs
- [x] PostgreSQL backup, restore, upgrade, and rollback documentation
- [x] Data-retention and cleanup jobs for sessions, provider payloads, attachments, and telemetry

### Operator notifications and backups in Telegram

- [x] Operator supplies a group ID and the bot creates and binds every required forum topic itself
- [x] Purchase, renewal, top-up, refund, fulfillment-failure, and incident events routed to their own topic
- [x] Topic rebinding, recreation after deletion, and clear failure states when permissions are missing
- [x] Notification volume controls so a burst cannot flood an operator group
- [x] Scheduled PostgreSQL backups with retention, encryption, and integrity verification
- [x] Backup status and restore initiated from the bot with confirmation, permission check, and audit event
- [x] No customer content, secret, or payment payload in any operator notification

### Maintenance mode

- [x] Maintenance mode with manual activation and automatic detection of Remnawave or panel unavailability
- [x] Maintenance mode blocks purchases and fulfillment while preserving already-paid state
- [x] Localized customer notice, expected-return messaging, and automatic exit on recovery

### Quality and compatibility

- [x] Testcontainers coverage for PostgreSQL, migrations, repositories, outbox, and River jobs
- [x] Contract tests against supported Remnawave 3.2.x behavior
- [x] Provider sandbox integration suites and replay fixtures
- [x] Load tests for update bursts, campaigns, webhook retries, and reconciliation
- [x] Upgrade test from every supported Omniflow migration baseline
- [x] Docker images pinned by version with SBOM, provenance, and vulnerability scan
- [x] Compose deployment tested with Caddy and Traefik examples
- [x] Complete operator setup, troubleshooting, and disaster-recovery docs

### Definition of bot/backend complete

- [x] A new operator can install Omniflow, import users, configure a plan and provider, and accept a real sandbox payment from Telegram
- [x] A successful payment is never lost when Remnawave is unavailable
- [x] Duplicate Telegram updates, callbacks, webhooks, and jobs cannot duplicate money or entitlement
- [x] Customers can purchase, connect, renew, manage devices, get support, and review history without a web panel
- [x] A customer can fund a wallet, keep a cart, and have it purchased automatically once the balance covers it
- [x] An operator receives purchase, renewal, top-up, and failure notifications and can restore a backup without a web panel
- [x] Enabling or disabling multiple concurrent subscriptions never orphans an entitlement or a Remnawave user
- [x] CI, security scanning, documentation validation, and release automation are green

---

## ✅ v0.6 — Admin panel foundation and access control

Goal: build the secure operator shell only after the Telegram/backend product is complete.

### Authentication and sessions

- [x] Secure first-owner bootstrap with one-time setup token
- [x] Password hashing with current recommended parameters
- [x] TOTP two-factor authentication and recovery codes
- [x] Session rotation, inactivity timeout, absolute expiry, logout-all, and device/session list
- [x] Login rate limiting and lockout/backoff
- [x] Security notifications for new sign-ins and credential changes, delivered to the
      operator Telegram group
- [x] CSRF protection, secure cookies, trusted proxy handling, and restrictive security headers
- [x] Password reset flow that does not disclose account existence — the token is
      logged for out-of-band delivery until email transport lands in v0.7
- [x] Optional OIDC configuration without making an external identity provider mandatory:
      discovery, PKCE, JWKS verification, verified-email requirement, and opt-in
      auto-provisioning that never adopts an existing address

### RBAC and audit

- [x] Owner, administrator, support, finance, marketing, and read-only auditor roles
- [x] Granular permissions enforced in the Go API
- [x] Permissions enforced at the Next.js server boundary, so an operator without a
      permission never receives the page
- [x] No authorization decisions based only on hidden routes or frontend state
- [x] Append-only audit events for authentication, authorization, and configuration actions
- [x] Actor, target, action, reason, timestamp, request ID, and safe before/after metadata
- [x] Audit search, filters, pagination, and export without secrets

### Shared admin application shell

- [x] Responsive `/admin` layout using shared shadcn primitives
- [x] Accessible navigation, command search, breadcrumbs, and keyboard operation
- [x] Russian and English `next-intl` catalogs
- [x] Generated Orval client, shared typed transport, and SWR cache policy
- [x] React Hook Form and Zod validation for all settings and mutations
- [x] Explicit skeleton, empty, partial, stale, permission-denied, and error states
- [x] Global notifications, confirmation dialogs, destructive-action safeguards, and
      unsaved-change protection
- [x] URL-backed filters, cursor pagination, sortable tables, and saved operator preferences

### Known limitation

- Panel pages call the shared typed transport rather than the generated SWR hooks.
  Orval 8.24's `client: "swr"` generator emits bare `fetch` calls and ignores
  `override.mutator`, so a generated hook carries neither the session cookie nor the
  CSRF token. Both target the same generated contract from `api/openapi.yaml`; only the
  transport differs. Revisit when the generator supports a mutator for this client.

### Verification debt

- [x] Testcontainers coverage for bootstrap, sign-in, lockout, session lifecycle,
      role changes, preferences, pagination, and the audit trail
- [x] `20260812000000_admin_foundation.sql` applied against PostgreSQL 18.4 from a bare
      database, and its checksum recorded with `atlas migrate hash`

---

## 🐞 Known defect carried from v0.5

`TestConcurrentSubscriptionPurchasesRespectTheLimit` fails: concurrent purchases are
not refused when they would exceed the configured per-customer subscription limit
(`internal/integrationtest/load_test.go:167`). Reproduced at `1f3199e`, so it predates
the v0.6 work and is unrelated to the admin panel. It needs a fix before 1.0 because
it lets a customer exceed a limit the operator configured.

---

## 🚧 v0.7 — Admin operations and commerce

Goal: let operators run day-to-day customer, subscription, and financial operations.

### Overview and system health

- [x] Dashboard for active/expired customers, traffic, renewals, payment health, open support, and failed jobs
- [x] Metrics with explicit definitions, timezone, comparison period, and data freshness
- [x] Remnawave, PostgreSQL, Valkey, Telegram, worker, and provider health
- [x] Recent incidents, reconciliation drift, webhook failures, and required actions
- [x] Traffic, purchase, refund, and referral anomaly detection with configurable thresholds
- [x] Anomaly alerts delivered to the operator topic with evidence and no automatic customer punishment
- [x] Anomaly review with threshold configuration, supporting evidence, acknowledgement, and dismissal
- [x] Maintenance-mode state, activation reason, and manual override

### Customers and subscriptions

- [x] Customer search by safe identifiers with status and segment filters
- [x] Customer profile with identities, subscription, devices, orders, wallet, referrals, support, consent, and audit timeline
- [x] Every concurrent subscription listed with independent lifecycle actions and its own Remnawave mapping
- [x] Create/link/import customer with duplicate detection
- [x] Suspend, reactivate, anonymize, and delete according to retention rules
- [x] Create, extend, enable, disable, reset traffic, change limits, and change squads through Remnawave API
- [x] Device review and removal without exposing identifiers unnecessarily
- [x] Bulk import/export with preview, validation errors, progress, resumability, and audit history
- [x] Bulk actions with permission checks, impact preview, limits, and per-item results
- [x] External blocklist source configuration, refresh schedule, and connection health
- [x] Blocklist match review with evidence, manual allowlist override, and appeal handling
- [x] Block reason, actor, and source recorded as an audit event for every decision

### Catalog and promotions

- [x] Plan list, create, version, archive, visibility, localization, and ordering
- [x] Price, currency, duration, traffic, device, squad, and eligibility configuration
- [x] Trial, upgrade, downgrade, grace-period, and renewal policies
- [x] Multiple-subscription enablement with per-customer and per-plan concurrency limits
- [x] Promotion and promo-code management with usage analytics
- [x] Plan-scoped squad sets, selection policy, and subscription-configurator visibility
- [x] Add-on catalog for traffic, device slots, and squads with versioned pricing and proration rules
- [x] Promo-code reward types for fixed amount, percentage, subscription days, and trial grant
- [x] Wallet-credit promo codes recorded as ordinary ledger entries
- [x] Stacking rules, precedence order, and explicit rejection reasons
- [x] Personal offers targeted at a single customer with validity window and single-use redemption
- [x] Offer presentation in the bot with expiry countdown, terms, and dismissal
- [x] Preview of customer-facing Telegram and web presentation

### Finance

- [x] Order and payment search, details, timelines, and provider references
- [x] Pending/stuck payment reconciliation and safe retry tools
- [x] Refund workflow with provider capability checks, reason, confirmation, and audit
- [x] Append-only wallet ledger and permission-gated corrective entries
- [x] Provider configuration with encrypted secrets, connection test, and webhook status
- [x] Wallet top-up configuration, limits, preset amounts, and enabled providers
- [x] Financial CSV export with stable schema and timezone/currency clarity
- [x] Revenue views separated from payment volume, wallet credits, and refunds

### Recurring payments and auto-renew

- [x] Per-provider recurring capability declared through the provider contract
- [x] Per-merchant override for providers that do not grant card binding to every merchant
- [x] Saved payment method referenced only by provider token, with no card data stored by Omniflow
- [x] Customer-visible saved methods, default selection, and removal
- [x] Auto-renew from a saved method or wallet balance with a configurable lead time
- [x] Failed-charge retry schedule, dunning notification, and automatic fallback to manual renewal
- [x] Auto-renew disabled by default and enabled only after explicit customer consent
- [x] Operator enablement per provider and per merchant with an explicit capability test
- [x] Saved-method, dunning, and auto-renew failure review in the admin panel

### Gifts

- [x] Purchase a subscription, add-on, or wallet credit for another person
- [x] Gift codes claimable by an unlinked recipient with claim, expiry, and revocation states
- [x] Telegram gift delivery with an optional sender message and privacy-safe recipient handling
- [x] Gift orders kept separate from the recipient's own order and payment history
- [x] Refund, abuse, and reclaim rules for unclaimed, expired, and disputed gifts
- [x] Gift order, claim, expiry, revocation, and refund management in the admin panel

### Digital goods shop

- [x] Provider-neutral digital-goods adapter with a Fragment-backed implementation
- [x] Telegram Premium catalog with supported durations and localized presentation
- [x] Telegram Stars catalog with operator-defined quantities or packs
- [x] Recipient Telegram username validation and explicit confirmation before payment
- [x] Purchase for the customer or for another username with a recipient review step
- [x] Operator-configured markup, rounding, and currency conversion over provider cost
- [x] Quoted price with an explicit expiry whenever the provider rate is volatile
- [x] Digital-goods orders kept separate from subscription orders and Remnawave entitlements
- [x] Idempotent delivery that cannot deliver Premium or Stars twice for one order
- [x] Delivery polling, delayed-delivery state, and provider failure classification
- [x] Automatic wallet refund when delivery fails permanently
- [ ] Wallet, promo, and cart support reusing the existing commerce pipeline — the wallet is applied to a shop order exactly as it is to a plan; promo codes and saved carts are not, because promotion applicability and cart quoting are both plan-scoped and extending them to digital goods is a catalog design decision rather than a wiring one
- [x] Bot shop navigation with catalog, details, confirmation, delivery progress, and history
- [x] Admin catalog, provider credentials, markup configuration, and order review
- [x] Encrypted provider credentials with spend limits and low-balance alerts
- [x] No recipient data retained beyond what delivery and support genuinely require

### Fulfillment and jobs

- [x] Fulfillment history and Remnawave drift view
- [x] Retry/cancel controls constrained by job state and idempotency rules
- [x] Dead-letter queue view with safe error details
- [x] Webhook event list, verification status, attempts, and replay-safe reprocessing
- [x] Outbox lag and unpublished-event diagnostics

### What v0.7 delivered

The operator workspace is usable end to end: an operator can read the dashboard
with its incidents, drift, and required-action list, search customers by a safe
identifier, work through a customer's subscriptions, orders, wallet, referrals,
support history, consent, risk history, and audit timeline, suspend or
reactivate them with a recorded reason, publish plan versions and add-ons,
manage promotions and targeted offers, search and export orders, refund an order
or re-poll a stuck payment, configure a payment provider and test its
credentials, retry or cancel a fulfillment job, replay a failed webhook,
adjudicate a blocklist match, review an anomaly, run a previewed bulk action
across hundreds of records, and edit the wallet and subscription settings that
were environment variables until now.

The three subsystems that had a data model and an operator surface but no
customer-facing half are now wired end to end.

**Gifts.** A customer can buy a subscription or wallet credit for somebody else
and pass on a claim code that exists in exactly one message; only its SHA-256 is
stored. Single redemption is a property of the claim predicate rather than of
timing, and every refusal renders the same message so a recipient cannot learn
which codes exist.

**Digital goods.** The bot shop sells Telegram Premium and Stars, quoting the
price when the product is opened so the number on screen is the number charged,
with a separate recipient review step because a mistyped username is
unrecoverable once a gateway has sent the goods. Delivery polls a submission
that already has a provider reference instead of submitting again.

The gateway that fronts Fragment honours no idempotency key. A lost answer is
therefore genuinely ambiguous, and such a delivery is resolved by neither retry
nor refund: it parks in the panel's review queue until an operator confirms with
the provider what happened. An adapter that cannot be polled produces the same
outcome, which is why the pipeline polls but this gateway still parks.

**Recurring payments.** The renewal worker charges without the customer present.
`dunning_attempts` is the schedule rather than a process, every attempt on a
cycle shares one order, an outstanding payment defers rather than fails, and
consent is re-checked at the moment of charging. Customers choose in the bot
what a renewal is charged to and how far ahead it runs, and manage their saved
methods.

One item stays deliberately unchecked. Promo codes and saved carts are not
extended to shop orders, because promotion applicability and cart quoting are
both plan-scoped, and pointing them at digital goods is a catalogue design
decision rather than a wiring one. The wallet is applied to a shop order exactly
as it is to a plan.

### Verification debt

- [x] `20260813000000_admin_operations.sql` applied against PostgreSQL 18.4 from
      a bare database, and its checksum recorded with `atlas migrate hash`
- [x] Unit coverage for anomaly evaluation and deduplication, blocklist
      normalisation and parsing, gift claim rules, digital-goods pricing and
      failure classification, and the recurring capability, lead-time, and
      dunning rules
- [x] Route tests proving every operations endpoint sits behind the session gate
      and that the surfaces are absent when no operations service is attached
- [x] `api/openapi.yaml` extended with the v0.7 panel operations, and both the
      Go server types and the Orval bindings regenerated from it
- [x] Testcontainers coverage for customer search, finance export, bulk-action
      preview and application, the recurring capability gate, single-delivery for
      shop orders, and single redemption for gifts
- [ ] Playwright coverage for the operator journeys, which arrives with the v0.8
      accessibility and browser gates

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

### Mandatory channel subscription

- [ ] Operator-configured required Telegram channels with per-channel enablement
- [ ] Membership verification before purchase and before subscription activation
- [ ] Periodic re-verification with automatic entitlement disable on unsubscribe
- [ ] Grace period, warning notification, and automatic restore on rejoin
- [ ] Exemption list, per-customer bypass, and a complete audit trail for every state change

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
- [ ] Generic OIDC sign-in configured from a discovery document, with no provider-specific code paths
- [ ] Several OIDC providers enabled at once with operator-controlled label, icon, and ordering
- [ ] Tested configuration presets for Google, Yandex, and Discord that remain ordinary OIDC entries
- [ ] Authorization Code flow with PKCE, state, and nonce validation
- [ ] Claim-to-identity mapping with an explicit verified-email requirement and configurable trust
- [ ] Linking an OIDC identity to an existing customer without implicitly merging two customers
- [ ] Unlink protection that refuses to remove the last usable sign-in method
- [ ] OIDC stays optional; Telegram sign-in never requires an external identity provider
- [ ] Provider outage, revoked consent, changed subject identifier, and email-change handling
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
- [ ] Subscription switcher when multiple concurrent subscriptions are enabled, hidden when they are not
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
- [ ] Explicit subscription targeting in every lifecycle flow when multiple subscriptions are enabled
- [ ] Promo-code entry and eligibility explanations
- [ ] Wallet balance and exact application breakdown
- [ ] Enabled payment-provider selection and hosted/embedded provider handoff
- [ ] Pending, successful, failed, cancelled, duplicate, and delayed-payment recovery
- [ ] Provisioning progress that survives refresh and duplicate submissions
- [ ] Order, payment, receipt, wallet, and refund history

### Digital goods shop

- [ ] Shop browsing, product details, and recipient entry under the same rules as the bot
- [ ] Checkout, delivery progress, and order history at parity with the bot
- [ ] Delivery failure, refund, and support-handoff states

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
