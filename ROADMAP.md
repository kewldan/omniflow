<!-- markdownlint-disable MD013 -->

# 🗺️ Omniflow roadmap

This document is the delivery contract for Omniflow. Versions are ordered by product dependency, not by estimated date:

```text
Telegram Bot + backend → Admin web panel → Customer web panel → 1.0 GA
```

Work may prepare shared foundations early, but a later product surface must not displace unfinished requirements from the current phase. A version is complete only when its listed behavior, migrations, documentation, security controls, and release gates are complete.

**Where the boundary is today.** v0.1 through v0.10 are complete on `main`: the
backend, the Telegram customer product, the production runtime, the whole
operator panel, and the whole customer web panel — sign-in, subscriptions,
checkout, the wallet, the digital-goods shop, support, news, referrals, loyalty,
and a customer's own data. v1.0 is release engineering rather than product
surface — compatibility, security review, reliability, and documentation — and
its documentation and community work is done.

One item remains, and it is not something a change to this repository can
produce: an external security reviewer. The other two were closed by doing them
rather than describing them. A restore drill against a real database is
`tools/restore-drill.sh`, and it passes against a live installation. The green
CI run is on `main`: every job, including the browser gate that had never
executed once since it was written.

Running the product found five defects no test in the repository could have
seen, two of them serious enough that neither web panel worked in a browser in
any shipped configuration. Making the gates green found four more, all of them
in the gates themselves. Closing the verification debt found four more again:
creating a campaign had never worked against a real database, audience expansion
queued nobody for almost every segment, the AI settings screen rendered its
warnings as untranslated keys and never blocked on a blocking one, and the
performance budget was wired to a script no job ran. All three accounts are worth
reading before trusting a checked box in an earlier phase — [what running it
found](#what-running-it-found), [what the green run
cost](#what-the-green-run-cost), and the closed items in each phase's
verification debt — because the boxes were accurate about the code and silent
about whether anyone had run it.

The pattern is the same every time, and it is the one thing to take from this
document: code that nothing calls is indistinguishable from code that works, and
only a caller tells them apart.

A few items shipped with a caveat stated in their own line, and each phase
records its remaining verification debt in its own section; nothing else is
outstanding behind a checked box.

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

## ✅ Known defect carried from v0.5 — closed in v0.7

`TestConcurrentSubscriptionPurchasesRespectTheLimit` was failing, and this section
outlived the fix. It was closed in `e0a2396` and the note was never updated.

The limit itself was never breached. `CreateOrder` runs under `Serializable` and
takes a transaction-scoped advisory lock on the customer before counting their
subscriptions, so two simultaneous purchases cannot both observe a stale total.
What the test asserted was that at least one racer had been refused by the
*domain* check — and under `Serializable` most racers are aborted by PostgreSQL
first, which is the stronger outcome, because the conflict was caught by the
database rather than by an application read that could have gone stale. The test
now asserts the property that matters: the limit holds, the successes match the
subscriptions that exist, and every other attempt was refused by one guard or
the other.

---

## ✅ v0.7 — Admin operations and commerce

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
- [x] Wallet, promo, and cart support reusing the existing commerce pipeline — a
      shop order carries the wallet, a promo code, and a saved cart through the
      same order, redemption, and cart tables a plan uses. The catalogue
      decisions it needed are recorded in the schema: a promotion applies to
      plans or to goods and never both, so no existing promotion widened on
      upgrade; a discount may not take an order below what the provider
      charges, enforced in the table as well as in Go; and a saved shop
      purchase re-quotes before any charge and never buys itself, because a
      provider quote expires and a plan price does not
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
- [x] Playwright coverage for the operator journeys, which arrived with the v0.8
      accessibility and browser gates — sign-in, the refusal of every panel route
      to an anonymous visitor, and the accessibility, layout, and localisation
      gates. The journeys behind the session gate need a seeded operator and are
      tracked in the v0.8 verification debt

---

## ✅ v0.8 — Complete admin panel

Goal: finish support, communication, configuration, and operational readiness before customer web development.

### Support desk

- [x] Ticket queues, assignment, priority, tags, status, SLA timestamps, and unread counts
- [x] Conversation view with safe attachment handling and operator replies delivered to Telegram/web
- [x] Internal notes distinct from customer-visible messages
- [x] Canned responses with localization and permission controls
- [x] Merge/duplicate handling, close/reopen, and complete audit history
- [x] Support workload and response-time reporting with documented definitions

### Referrals and loyalty

- [x] Referral program enablement, qualification rule, inviter/invitee reward, cap, and validity period
- [x] Attribution, qualification, rejected/fraud state, and reward history
- [x] Manual review and correction through compensating ledger entries
- [x] Loyalty tiers/rules with versioned definitions and deterministic evaluation
- [x] Abuse signals and rate limits without opaque automatic account punishment

### News, campaigns, and communication

- [x] Localized news posts with draft, preview, schedule, publish, unpublish, and archive
- [x] Audience segments using explicit, reviewable filters
- [x] Campaign preview, estimated audience, schedule, pause, cancel, and results — the panel shows the segment's definition in words beside its live count before a draft is created; test delivery to a single operator account remains and is tracked in the verification debt below
- [x] Transactional and marketing templates with variables validated before send
- [x] Consent, suppression list, quiet hours, frequency caps, and delivery deduplication
- [x] Delivery states for queued, sent, failed, blocked, and clicked where supported
- [x] No storage or telemetry of message content outside documented product data

### Mandatory channel subscription

- [x] Operator-configured required Telegram channels with per-channel enablement
- [x] Membership verification before purchase and before subscription activation
- [x] Periodic re-verification with automatic entitlement disable on unsubscribe
- [x] Grace period, warning notification, and automatic restore on rejoin
- [x] Exemption list, per-customer bypass, and a complete audit trail for every state change

### AI-assisted support

- [x] Provider-neutral AI gateway with OpenAI-compatible, Anthropic, and Gemini adapters
- [x] OpenAI-compatible adapter supports operator-selected hosted or local models
- [x] Separate model, temperature, token limit, timeout, and budget configuration per task
- [x] Ticket-thread summary with customer intent, actions already attempted, and unresolved questions
- [x] Suggested ticket reply grounded in the visible conversation and approved knowledge sources
- [x] Rewrite controls for shorter, clearer, friendlier, more formal, or more technical replies
- [x] Russian/English translation that preserves product names, links, and template variables
- [x] Suggested category, priority, tags, sentiment, and escalation reason
- [x] Similar-ticket and relevant documentation suggestions with source citations
- [x] Secret, token, subscription-link, payment-data, and unnecessary-PII redaction before model requests
- [x] Generated replies remain editable drafts; an authorized operator must explicitly send them
- [x] Clear AI-generated label, provider/model disclosure, retry, cancellation, and fallback states

### Scam, abuse, and social-engineering analysis

- [x] Explainable risk analysis for scam, phishing, impersonation, social engineering, payment fraud, and referral abuse
- [x] Evidence list, confidence, uncertainty, and matched policy signals instead of an unexplained score
- [x] Cross-ticket pattern detection using minimized, permission-safe structured signals
- [x] Suspicious-link and attachment metadata checks through explicitly configured tools
- [x] Operator feedback for false positive, confirmed abuse, and insufficient evidence
- [x] Human review required before suspension, refund denial, wallet correction, or other adverse action
- [x] AI output can recommend an action but cannot silently punish a customer or mutate financial state
- [x] Risk models, prompts, thresholds, and policy versions recorded with each assessment

### AI writing and marketing tools

- [x] Draft news posts, service announcements, campaign messages, subjects, and calls to action
- [x] Rewrite, shorten, expand, simplify, change tone, and produce operator-requested variants
- [x] Translate and localize between Russian and English rather than performing literal translation only
- [x] Preserve and validate template variables, Markdown/HTML rules, Telegram limits, and channel constraints
- [x] Brand-voice, forbidden-claims, consent, quiet-hours, and communication-policy checks
- [x] Readability, ambiguity, spam-likelihood, and potentially misleading language review
- [x] Audience-aware suggestions using aggregate segment definitions without exposing raw customer lists
- [x] Side-by-side diff, undo, version history, and explicit operator acceptance of generated edits
- [x] AI may prepare a campaign draft but cannot select recipients, schedule, or send without confirmation

### Admin copilot

- [x] Permission-aware assistant for explaining dashboards, failed jobs, webhook errors, and reconciliation drift
- [x] Natural-language search over customers, orders, tickets, and audit history using authorized structured tools
- [x] Answers cite the records, metrics, documentation, or tool results used to produce them
- [x] Suggested next actions deep-link to the normal admin workflow rather than bypassing it
- [x] Read-only by default; every mutation requires a preview, permission check, reason, and confirmation
- [x] No autonomous payment, refund, wallet, entitlement, suspension, campaign, or role mutation

### MCP integration

- [x] MCP client with remote Streamable HTTP transport and standards-based authorization
- [x] Discover MCP tools, resources, and prompts with cached capability metadata and health status
- [x] Owner-managed MCP server registry with explicit enablement, allowlists, timeouts, and egress restrictions
- [x] Encrypted MCP credentials that are write-only in the admin interface
- [x] Per-server and per-tool mapping to Omniflow RBAC permissions
- [x] JSON Schema validation for every tool input and output before it reaches AI or application code
- [x] Tool-call preview showing server, tool, arguments, affected records, and expected side effects
- [x] Human confirmation for external writes and every consequential Omniflow mutation
- [x] First-party Omniflow MCP server for permission-scoped admin tools, resources, and operational documentation
- [x] First-party MCP server is read-only by default; mutation capabilities are separately enabled and audited
- [x] Prompt-injection defenses that treat tickets, webpages, attachments, MCP resources, and tool output as untrusted data
- [x] Tool recursion, call-count, duration, response-size, and monetary-cost limits
- [x] Circuit breakers and graceful degradation when an MCP server is unavailable or returns invalid data
- [x] Full audit trail for connection changes, model requests, tool calls, confirmations, results, and failures

### AI privacy, governance, and cost controls

- [x] AI and MCP are globally optional and disabled until an owner configures them
- [x] Per-feature enablement for support, marketing, scam analysis, copilot, and MCP tools
- [x] Data-use preview shows operators exactly which fields will leave the installation
- [x] Provider-specific retention/training warnings and documented zero-retention options where available
- [x] Configurable prompt/output retention with deletion and legal-hold behavior
- [x] AI prompts, outputs, ticket content, customer data, and tool arguments never enter anonymous telemetry
- [x] Per-role, per-operator, per-feature, and installation-wide usage limits
- [x] Token, request, latency, error, and estimated-cost reporting without exposing prompt content in metrics
- [x] Provider/model fallback is explicit and cannot route data to an unapproved provider
- [x] Audit exports identify generated content and consequential decisions influenced by AI

### AI and MCP quality gates

- [x] Sanitized evaluation sets for support replies, translations, marketing edits, and scam analysis
- [x] Regression thresholds for correctness, groundedness, citation validity, tone, and unsafe advice
- [x] Prompt-injection, tool-confusion, data-exfiltration, privilege-escalation, and indirect-injection tests
- [x] Tests proving operators cannot invoke tools beyond their own RBAC permissions through AI or MCP
- [x] Tests proving duplicate or retried tool calls cannot duplicate financial or provisioning effects
- [x] Model/provider outage, timeout, malformed output, budget exhaustion, and partial-tool-failure tests
- [x] AI features degrade to normal manual admin workflows without blocking support or operations

### Settings and operations

- [x] General branding, service name, support contacts, locale, timezone, and public URLs
- [x] Remnawave connection, compatibility check, reconciliation schedule, and safe token rotation
- [x] Telegram bot identity, webhook, command, and delivery status
- [x] Operator group and topic binding with automatic topic creation and permission diagnostics
- [x] Mandatory channel list, verification schedule, grace period, and exemption management
- [x] Maintenance-mode policy, dependency detection thresholds, and customer notice text
- [x] Payment-provider configuration and capability matrix
- [x] Notification thresholds and templates — test delivery to a single operator account remains and is tracked in the verification debt below
- [x] AI providers, model routing, budgets, privacy controls, evaluation status, and connection tests
- [x] MCP server registry, authorization, capability allowlists, health, and audit history
- [x] Telemetry status, exact payload preview, and global opt-out
- [x] Operator, role, session, and security settings
- [x] Backup schedule, retention, encryption, restore history, and verified test restore
- [x] Backup status, version, migration status, and diagnostics bundle — update availability needs a release feed and is tracked in the verification debt below
- [x] Secrets never returned after write and always excluded from diagnostics

### Definition of admin complete

- [x] An owner can configure and operate every backend capability without SQL or shell access
- [x] Support and finance roles can do their jobs without receiving unrelated permissions
- [x] Every sensitive mutation is authorized, validated, confirmed where necessary, and audited
- [x] AI-assisted work is grounded, editable, attributable, and never required for a manual workflow
- [x] MCP tools cannot bypass RBAC, mutation confirmation, idempotency, or audit requirements
- [x] Testcontainers cover repositories and critical workflows
- [x] Playwright covers admin authentication and highest-risk operator journeys
- [x] Accessibility, responsive layout, localization, and browser support gates pass

### The AI and MCP boxes describe code, not a reachable surface

Stated here in v1.0 because the documentation review that closed out this phase
made it unavoidable, and a checked box that a reader cannot act on is worse than
an unchecked one.

Everything ticked in the four AI sections and the MCP section above is
implemented and unit-tested — the gateway, the redactor, the support, marketing,
risk, copilot, and evaluation packages, the MCP client and the first-party MCP
server, and the settings screens that register a provider, enable a feature,
route it to a model, and bound what it may spend. What did not exist was the
wiring between them: no process constructed a gateway or an MCP client, nothing
mounted an AI route on `/v1/panel`, and no screen outside `/admin/settings/ai`
called one. `internal/aigateway` was imported by the feature packages and by
nothing else; `internal/mcpserver` is still imported by nothing at all.

**The join now exists, and one thing uses it.** `internal/airuntime` builds an
`*aigateway.Gateway` for a named feature out of what an owner configured: the
approved provider, its unsealed credential, the model, the temperature, the
timeout, and the budget, resolved on every call so a rotated key takes effect on
the next click rather than at the next restart. A gateway is built per feature
rather than one holding every task, because two features share the
`reply_suggest` task and a task-keyed map could hold only one of their
configurations. The budget is read against the feature and enforced before the
request; the meter deliberately records nothing, because the caller is the only
party that knows the operator, the latency, and whether the call was refused,
and two writers would double every figure in the cost report.

The first caller is the connection test — the smallest instance of the gap, and
the one the debt below named. `POST /v1/panel/settings/ai/providers/{slug}/test`
opens the credential, asks the provider for one word, and records the outcome
through `RecordAIProviderCheck`, so *Never connection-tested* is a state an owner
can leave. It is a real completion rather than a reachability probe, because an
address that resolves and a key that is accepted are separate facts and only the
second is the question. It sits behind `settings.write`, is rate limited per
operator, audits as `ai.provider.tested`, and distinguishes a test that ran and
failed — 200, recorded — from one that could not be attempted — 4xx, nothing
stored, because recording a failure would claim something was tried.

Reading that screen to wire the button found a defect in it.
`aigovernance.Warning` carried no JSON tags, so it went out as `Code`, `Text`,
and `Blocking` while the panel read `code`, `text`, and `blocking`. Every
warning rendered as an untranslated key, and `blocking` read as `undefined` —
which means the guard that refuses to switch on a feature with no provider was
present, rendered, and inert for as long as the screen has existed.

What remains unwired is the rest: no support, marketing, risk, or copilot feature
reaches a model, and no MCP client is constructed. That is still a phase of its
own. `docs/index.mdx` now states the boundary exactly — a model call happens when
an operator presses *test connection*, and at no other time.

The one consequence worth naming for the release gates: "AI features degrade to
normal manual admin workflows without blocking support or operations" holds
trivially rather than by design, because there is nothing to degrade from.

### Verification debt

Carried into v0.9 and tracked here rather than left implied.

- **AI features have no runtime surface**, as set out above, and MCP has none at
  all. ~~The connection test is the smallest instance of it: `RecordAIProviderCheck`
  and `AIProviderCredential` exist and the panel renders their result, but
  nothing calls either, so every provider reads *Never connection-tested*
  forever.~~ Closed. `internal/airuntime` builds a gateway from stored
  configuration and the panel's test button uses it. The support, marketing,
  risk, and copilot features, and every MCP path, remain unwired.
- ~~**Campaign and notification test delivery.** Scheduling, pausing, cancelling,
  audience estimation, and result counters are implemented and enforced; sending
  one message to a single operator account before committing to the audience is
  not. It needs a delivery path that reaches the outbox without creating
  recipients, so the test send cannot be mistaken for the campaign in the
  counters.~~ Closed. `20260824000000_campaign_test_sends.sql` gives the preview
  a queue of its own, and the delivery path is the operator notifier's, into a
  forum topic of its own. Nothing touches `campaign_recipients`, which is what
  keeps the counters honest and keeps a preview from consuming a place in the
  audience. The rendering is shared with the customer delivery path rather than
  reimplemented, because a preview that substituted variables differently would
  be a preview of a different message.

  Writing the tests for it found two defects that had made the surface
  unusable, and both were invisible from the panel. **Creating a campaign never
  worked at all**: `CreateCampaign`, `SetCampaignState`, and both suppression
  calls audited under the category `communication`, which
  `audit_events_category_known` does not allow, so the insert failed and took
  the whole transaction with it. Nothing in the repository had ever created a
  campaign against a real database. And **audience expansion queued nobody** for
  any segment binding a value: the statement passed the campaign id as `$1` and
  appended the segment's arguments after it, while the compiled filter numbers
  from `$1` too. That one was known and documented as a warning on the campaigns
  page rather than fixed; it is fixed, and an integration test expands a
  `locale` segment and fails without the fix.
- ~~**Update availability.** The diagnostics bundle reports the running version,
  the schema state, and every applied migration. It does not say whether a newer
  release exists, because that needs a release feed the installation would have
  to reach, which is a network dependency and a disclosure decision an owner
  should make deliberately rather than inherit.~~ Closed, on exactly those
  terms. `internal/updatecheck` reads a feed named by `APP_UPDATE_FEED_URL` and
  adds an `update` line to the bundle. There is no default address: a built-in
  one would make the disclosure decision for the owner, and it would also be an
  address the documentation cannot corroborate, because these docs name no
  repository. A feed that refuses, times out, or answers with nothing usable
  reports `unreachable` rather than "up to date", the detail is a category
  rather than the transport's message — a feed URL can carry a token — and the
  feed is read at most once every six hours however often the bundle is
  generated.
- ~~**Playwright coverage behind the session gate.**~~ Closed in v1.0. CI's
  `e2e` job now applies the migrations, starts a real API, and redeems the
  one-time setup token to seed an operator, so `operator-journey.spec.ts` signs
  in for real, proves the session survives a navigation, and sweeps every panel
  page for untranslated message keys. Leaving it undone had hidden three defects
  at once; see [What running it found](#what-running-it-found). The customer half
  still needs a seeded customer with a Remnawave user behind it and remains open
  under v0.10.
- **Evaluation runs are manual.** The sets ship with the binary and the
  thresholds are enforced by `Report.Regressions`, but nothing schedules a run
  against an installation's own configured provider. That is deliberate for now:
  running it automatically would spend an owner's budget without being asked.


---

## ✅ v0.9 — Customer web foundation

Goal: expose the proven customer capabilities through the shared web application.

### Authentication and account security

- [x] Telegram-based sign-in linked to the same canonical customer identity —
      the login widget in a browser, and the Mini App's signed `initData` when
      the panel is opened inside Telegram
- [x] Time-limited magic-link fallback where enabled by the operator, delivered
      by the bot rather than requested from a web form
- [x] Generic OIDC sign-in configured from a discovery document, with no provider-specific code paths
- [x] Several OIDC providers enabled at once with operator-controlled label, icon, and ordering
- [x] Tested configuration presets for Google, Yandex, and Discord that remain ordinary OIDC entries
- [x] Authorization Code flow with PKCE, state, and nonce validation
- [x] Claim-to-identity mapping with an explicit verified-email requirement and configurable trust
- [x] Linking an OIDC identity to an existing customer without implicitly merging two customers
- [x] Unlink protection that refuses to remove the last usable sign-in method
- [x] OIDC stays optional; Telegram sign-in never requires an external identity provider
- [x] Provider outage, revoked consent, changed subject identifier, and email-change handling
- [x] Session rotation, logout-all, inactivity/absolute expiry, CSRF protection, and secure cookies
- [x] Account-link conflict handling without revealing another customer’s existence
- [x] Customer-visible active sessions and security events
- [x] Suspended, deleted, and unlinked account states

### Shared customer shell

- [x] Responsive mobile-first layout sharing the admin component system
- [x] Russian and English localization with persisted preference
- [x] Accessible navigation, keyboard behavior, focus handling, and reduced motion
- [x] Typed SWR data loading and Zustand only for complex local workflows — no
      screen has yet needed Zustand, which is the intended outcome rather than
      an omission
- [x] Explicit loading, empty, stale, offline, partial, and error states
- [x] Secure handling of subscription links with no analytics or accidental preview leakage

### Dashboard and subscription

- [x] Status, expiry, remaining days, traffic, device usage, and active plan
- [x] Subscription switcher when multiple concurrent subscriptions are enabled, hidden when they are not
- [x] Traffic visualization with accessible textual equivalent
- [x] Subscription open/copy, QR/deep-link connection, and platform instructions
- [x] Subscription-link rotation with reauthentication/confirmation
- [x] Device list, per-device removal, and remove-all confirmation
- [x] Renewal, expiry, grace-period, and incident notices

### What v0.9 delivered

A customer can now open `/account` in a browser, sign in as the person the bot
has been talking to, and see the subscription they already have — the same
records, the same entitlements, the same Remnawave state. It is one product with
two surfaces, not two products.

**Sign-in is three routes onto one identity.** Telegram's login widget is
verified against the bot token with a one-minute freshness bound, because the
hash itself never expires and a payload captured from a URL would otherwise
authenticate forever. Inside Telegram the Mini App's `initData` does the same job
under a different key derivation, so the panel signs in with no button at all.
OIDC is Authorization Code with PKCE, state, and a nonce checked against the
returned ID token; several providers run at once, and the shipped Google, Yandex,
and Discord presets are values that prefill a form rather than code paths.

The link fallback is requested from the bot, not from a web form. A form would
have to take an identifier and answer whether Omniflow knows it, and every
version of that answer tells a stranger whether somebody is a customer here.
Starting inside a chat the customer has already authenticated to removes the
question, and guarantees the link can only reach somebody who already controls
the account.

**Two rules keep accounts from merging by accident.** A subject nobody has linked
never adopts an existing customer by email address — matching on an address would
let anyone who can make a provider assert somebody else's address take over their
account. And linking happens from inside a session the customer already holds; a
subject that belongs to somebody else is refused as a conflict rather than
merged. Because the subject is the key and the address is only a claim, an
upstream email change is a non-event with no recovery path to write.

**Customer sessions are not operator sessions.** Fourteen days idle and sixty
absolute, against thirty minutes and twelve hours for the panel. An operator
signing in again costs seconds of a working day; a customer checking their
remaining days does not warrant the same friction, and a panel that logs them out
over lunch just pushes them back to the bot. The absolute horizon, 24-hour token
rotation, and a fifteen-minute re-authentication window on the three destructive
actions — rotating the access link, disconnecting every device, removing a
sign-in method — do the security work instead.

**Privacy is enforced at the transport, not per screen.** A device is named by an
opaque digest the server resolves against its own current list, so no hardware
identifier or IP address ever reaches the browser and removing a device never
requires the caller to hold the identifier being acted on. Every `/v1/account`
response carries `no-store` and `no-referrer`, because a subscription link is a
credential; the QR code is generated in the browser rather than fetched, which
would otherwise put that credential in a request line, a proxy log, and the
cache.

The customer's account history is a separate record from the operator audit
trail. The audit trail is searched under operator permissions, and deciding per
row which of its fields a customer may see would put a disclosure decision in the
read path. The customer-facing log is written to be shown: a closed vocabulary,
and no column for an amount, a link, or another party's identifier.

Two changes fell outside the customer surface and are worth naming. The session
token construction moved to `internal/websession` so both panels share one
implementation and one storage discipline rather than two copies. And the shared
`--subtle-foreground`, `--success`, and `--warning` tokens were darkened: the
source design's values reach 2.5:1 on white, which failed the accessibility gate
on the pre-existing pages as well as the new ones.

### Verification debt

- **Playwright coverage behind the session gate — the customer half.** The
  browser suite proves every account route refuses an anonymous visitor, that a
  failed sign-in explains itself, and that the accessibility, layout, and
  localisation gates hold on the pages reachable without signing in. The
  operator half of this debt was closed in v1.0 by having CI run a real API and
  seed an operator; the customer half is harder and is still open, because a
  customer session needs a Remnawave user behind it and CI has no panel to point
  at. A stub adapter is the likely answer rather than a live instance.
- **The OIDC round trip is not exercised end to end.** Claim mapping, the unlink
  guard, provider-scoped revocation, and the conflict rule are covered against a
  real database, but the authorization round trip itself needs a live provider or
  a stub issuer serving a discovery document and a JWKS. That stub belongs with
  the integration harness rather than in a unit test.
- ~~**`go test -race` could not be run on the development machine.**~~ The race
  detector still fails to link against the local MinGW toolchain, but that is an
  argument for changing where it runs rather than for leaving it unverified:
  `go test -race ./...` passes in a `golang:1.26.5-alpine` container against the
  same working tree, which is the same thing CI does and needs no toolchain on
  the host. `sqlc generate` has the same limitation and the same answer, through
  `sqlc/sqlc:1.31.1`.
- **The panel has no screen for customer OIDC providers yet.** ~~The API is
  complete, gated by `settings.write`, and audited, but an operator configures a
  provider through the API rather than through a form.~~ Closed in v0.10: the
  form ships at `/admin/settings`, with the shipped presets as one-press
  starting points and the client secret write-only end to end.

### Known defect found during v0.9 — closed in v0.10

Two operator browser checks failed against a build with no API behind it. Both
are fixed. The sign-in page's card title is now the page's `h1` rather than an
`h2`, because a document whose outline starts at level two gives a screen-reader
user no landmark to jump to and that page is nothing but a form. The failed
sign-in assertion was matching Next's route announcer as well as the form's own
alert; it is now scoped, and the fill it depends on retries until the value
survives hydration — a race WebKit hit reliably and Chromium usually missed,
which had the test reporting a missing error message when what it had actually
lost was a keystroke.

---

## ✅ v0.10 — Complete customer web panel

Goal: reach feature parity with the customer-facing bot while taking advantage of web interaction patterns.

### Plans and checkout

- [x] Plan comparison with localized terms and transparent price/currency/period
- [x] Trial, purchase, renewal, upgrade, and downgrade flows
- [x] Explicit subscription targeting in every lifecycle flow when multiple subscriptions are enabled
- [x] Promo-code entry and eligibility explanations
- [x] Wallet balance and exact application breakdown
- [x] Enabled payment-provider selection and hosted/embedded provider handoff
- [x] Pending, successful, failed, cancelled, duplicate, and delayed-payment recovery
- [x] Provisioning progress that survives refresh and duplicate submissions
- [x] Order, payment, receipt, wallet, and refund history — a top-up is reachable
      by its own identifier but is not listed among purchases, because it buys
      nothing and is already accounted for in the wallet's ledger

### Digital goods shop

- [x] Shop browsing, product details, and recipient entry under the same rules as the bot
- [x] Checkout, delivery progress, and order history at parity with the bot
- [x] Delivery failure, refund, and support-handoff states

### Support and communication

- [x] Support ticket list, create, conversation, attachment, reply, close, and reopen
- [x] Read/unread state synchronized with Telegram where possible
- [x] News and service-announcement inbox
- [x] Notification preferences, marketing consent, and unsubscribe controls
- [x] Browser notifications only after explicit customer permission — foreground
      only, through the Notification API. Web push would need a service worker, a
      VAPID key pair, and a stored subscription per browser, which is an
      infrastructure and disclosure decision rather than a screen

### Referrals and account

- [x] Referral link/code, share action, terms, invited/qualified counts, and reward history
- [x] Loyalty status and progress when enabled
- [x] Profile, locale, timezone, contact channel, and privacy settings
- [x] Personal-data export request and account deletion workflow
- [x] Clear handoff to support for identity conflicts and irreversible actions

### Customer-web release gates

- [x] Playwright coverage for sign-in, subscription, checkout, device security,
      referral, and support journeys — every account route refuses an anonymous
      visitor, and the accessibility, layout, and localisation gates run against
      Chromium, WebKit, and a mobile viewport. The journeys behind the session
      gate need a seeded customer and are tracked in the verification debt below
- [x] Cross-surface contract tests proving Telegram and web produce the same domain outcomes
- [x] WCAG 2.2 AA review of core journeys — axe runs at 2.2 AA against the pages
      reachable without a session; the same fixture debt bounds the rest
- [x] Responsive testing for supported mobile, tablet, and desktop widths
- [x] No duplicate business rules in React; API remains authoritative
- [x] Performance budgets for JavaScript, images, server response, and core web
      vitals — **JavaScript only.** `bun run perf:budget` fails the build when a
      route's first-load JavaScript exceeds its area's ceiling, read from Next's
      own per-route figures. Images, server response, and field core web vitals
      are not measured, because they need a running installation with real
      content and real clients; a number invented from a local build would read
      as a gate that had passed

### What v0.10 delivered

A customer can now do everything in a browser that they could do in the chat.
They can compare plans, start a trial, buy, renew, upgrade, downgrade, enter a
promo code and be told exactly why it was refused, see the wallet applied against
the price to the minor unit, pick a payment method, be handed off to it, come
back, and watch provisioning finish. They can top up, read their ledger, buy
Telegram Premium for somebody else, open a support ticket with an attachment,
read the announcements, set what they are willing to be messaged about, share a
referral link, see their loyalty tier, download everything the installation holds
about them, and ask for the account to be deleted.

**The bot's checkout is now the panel's checkout.** The customer-side
orchestration moved out of `internal/botapp` into `internal/accountcheckout`, and
the bot delegates to it — the session, the quote, the promo evaluation, the
order, the payment handoff. This is not tidiness. Two implementations of a
purchase eventually price the same order differently, and only one of the two
customers finds out. The checkout session is deliberately shared: a customer has
at most one, so opening one in a browser supersedes one left open in a chat
rather than running beside it.

**Three refusals that had to be built rather than avoided.** A digital-goods
price is a quote with an expiry, so the purchase echoes back the price that was
displayed and the server re-quotes before charging — refusing distinctly when the
window lapsed and when the number moved, because charging a price nobody saw is
the failure the whole flow exists to prevent. A recipient is confirmed in its
normalised form in a step of its own, because delivery is irreversible the moment
a gateway has sent the goods. And an ambiguous delivery is resolved by neither
retry nor refund: it parks for an operator, and there is no retry control in the
API or in the UI, because the gateway honours no idempotency key and retrying
could deliver and charge twice.

**Deletion is a request.** Every screen says so. It appends a lifecycle event
with the customer as its actor and nothing else; the retention workflow an
operator already governs is what deletes. The export, by contrast, is produced
synchronously — a queued export would need a delivery channel, a retention
window, and a self-authenticating link, three further disclosure decisions to
answer a question that fits in one response. It declares its own redactions and
names any section it truncated.

**Two things the customer never learns.** A contact address already registered
elsewhere is refused as "not available here" and nothing more, because any richer
answer turns the panel into an oracle for whether a given address belongs to a
customer. And a support conversation is read from tables that never include
`support_notes` — keeping operator notes out by never querying them is a stronger
guarantee than filtering them out would be.

### Defects found and fixed during v0.10

Each was the same shape: a column added in a later phase that an older code path
never learned about.

- **`/v1/account` was never mounted.** v0.9 built the customer handlers and did
  not pass them to the router, so the entire customer API existed only in tests.
- **The bot showed withdrawn news.** `news_posts.status` arrived in v0.8; the
  bot's visibility predicate still checked only `published_at`, so unpublishing a
  post hid it on the web and left it in Telegram. Its ordering also lacked a tie
  breaker, so a batch published in one instant could list differently on the two
  surfaces.
- **The bot counted reversed referral rewards as earned.** `reversed_at` arrived
  in v0.8. Worse than the wrong total: the count feeds the inviter reward cap, so
  reversing a fraudulent referral did not give the slot back.
- **Uploaded attachment files were never reclaimed.** Retention deleted the rows.
  Files are content-addressed, so one file can back several rows; the sweep now
  removes a file only when no surviving row references it.
- **The shared transport mishandled two cases.** It declared
  `Content-Type: application/json` over a `FormData` body, destroying the
  multipart boundary, and it parsed every error body as JSON — so a proxy's own
  413 page reached the screen as an unknown error with the status lost.

### Verification debt

- ~~**Playwright coverage behind the session gate — the customer half**, as set
  out in v0.9's section.~~ Closed, and the fixture turned out to be cheaper than
  three phases of deferral assumed. It does not need a seeded customer with a
  Remnawave user: the customer panel provisions an account on first sign-in, so
  `customer-journey.spec.ts` signs a Telegram login-widget payload with the same
  bot token CI configures the API with, posts it through the web server's own
  `/v1` proxy, and gets a real session in the browser's cookie jar. That
  exercises the two things a session fixture would have skipped — the signature
  check and the cookie the browser has to keep — and both are where the operator
  half's defects were. `internal/customerauth` carries an interop test asserting
  that the TypeScript signing and the Go verification agree, so a mismatch fails
  as a mismatch rather than being "fixed" by weakening the check.

  What stays open is narrower than the original line: nothing behind a Remnawave
  entitlement is covered. The customer the suite creates has signed in and bought
  nothing, which is a real state — it is every customer's first minute — and the
  screens needing a subscription render their empty state. The WCAG 2.2 AA review
  is bounded the same way.

  The panel pages an operator sees are also swept for untranslated keys, which is
  how two missing translations that had been shipping since v0.7 were found —
  and, once the sweep first ran against a real API, how the sweep's own inability
  to tell a missing message from an audit action name was found.
- ~~**Attachment storage is a workaround.**~~ Closed.
  `20260823000000_attachment_storage.sql` gives `support_attachments` the
  `origin` and `storage_key` columns the web upload always needed, backfills the
  rows that carried their key in `telegram_file_id` behind a `web:` prefix, and
  constrains the pairing so a row cannot name one origin and carry the other's
  reference. The blocker was stated as `atlas` being unavailable in this
  environment; it was the same limitation as `go test -race` and `sqlc generate`
  and has the same answer, through `arigaio/atlas:1.3.0`. The migration was
  applied against a real PostgreSQL 18.4 both as an upgrade — proving the
  backfill converts a legacy row and leaves a Telegram one alone — and from a
  bare database to head.

  Removing the prefix surfaced a defect the prefix had been hiding. Two things
  deleted expired attachment rows: the worker's sweep, which reclaims the files
  no surviving row references, and a purge at the end of every bot notification
  pass, which reclaimed nothing. Whichever ran first won, so the file leak v0.10
  believed it had fixed was still reachable whenever the bot got there first. The
  bot's purge is gone; retention has one owner.
- ~~**Performance budgets cover JavaScript only**, as stated in the gate above.~~
  Partly closed, and the debt was worse than the line said: `perf-budget.mjs`
  was wired to a package script no job invoked, so the budgets gated nothing at
  all. CI now runs it in the `web` job, and it covers the stylesheet payload and
  the self-hosted fonts as well as first-load JavaScript — the two other things
  the build ships on every route's critical path. Images, server response time,
  and field core web vitals remain unmeasured, and that stays deliberate: they
  need a running installation with real clients, and a figure invented from a
  local build would read as a gate that had passed.
- **Two transient divergences are by design and worth knowing.** An operator
  reply raises the web's unread count immediately but does not move the bot's
  badge until the Telegram delivery worker runs, so a customer holding both
  surfaces sees a window where the two disagree; the domain contract — read on
  either surface is read on both — holds in both directions. And a wallet balance
  is reserved on a pending order rather than debited, so two pending orders can
  each claim the same balance and the second is caught at settlement. Both
  surfaces behave identically in both cases.

---

## 🚧 v1.0 — General availability

Goal: publish a stable release suitable for public single-server production use.

### Compatibility and upgrades

- [x] Published compatibility matrix for Omniflow, Remnawave, PostgreSQL, Valkey, Go, Bun, and browsers
- [x] Semantic versioning, changelog, signed release artifacts, container images, SBOM, and provenance
- [x] Automated upgrade tests and documented backup/restore/rollback procedure
- [x] Migration policy and supported upgrade window
- [x] Deprecation policy for API, environment variables, database behavior, and integrations

### Security and privacy

- [x] Threat model covering identity, Telegram, payments, webhooks, admin RBAC, SSRF, AI, MCP, prompt injection, and supply chain
- [ ] Independent security review of authentication, authorization, payments, and secret handling
- [x] Dependency, secret, SAST, container, and license scans enforced in release CI
- [x] Rate limits and abuse controls verified under load
- [x] Public privacy documentation, retention defaults, telemetry contract, and complete opt-out verification
- [x] AI/MCP data-flow inventory, provider disclosures, retention controls, and permission review
- [x] Security reporting and supported-version policy

The independent review is the one item here that isn't something a change to
this repository can produce: it names an external reviewer engagement, not
code or documentation. Everything else — the maintainers' own threat model at
[`docs/architecture/threat-model.mdx`](./docs/architecture/threat-model.mdx),
release-CI scanning, a load-verified rate limiter, the public
[`docs/operations/privacy.mdx`](./docs/operations/privacy.mdx) page, and the
AI/MCP data-flow inventory in `docs/operations/ai-governance.mdx` — is in
place and stays honest about the one place a documented control isn't
enforced yet: several session and security-event retention windows are
documented but not swept by any job, called out in both `security.mdx` and
the new privacy page rather than left implied.

### Reliability and operations

- [x] Defined service-level indicators for API, bot, jobs, payments, fulfillment, and notifications
- [x] Dashboards and alerts for actionable failure modes
- [x] Backup restoration drill and disaster-recovery runbook — the runbook is
      [`docs/operations/backup-restore.mdx`](./docs/operations/backup-restore.mdx)
      and the drill is `tools/restore-drill.sh`, which performs it rather than
      describing it and exits non-zero when it fails
- [x] Bounded retries, dead-letter handling, and reconciliation for every external side effect
- [x] Graceful degradation when Valkey, Remnawave, Telegram, or a payment provider is unavailable
- [x] Capacity guidance for small, medium, and large single-server installations

Automatic maintenance detection now watches Valkey alongside PostgreSQL and
Remnawave — `internal/maintenance/controller.go`'s default `Watch` list was
the only thing missing; `EvaluateMaintenance` and the `valkey` source already
supported it. Notifications is the one surface with no service-level
indicator: there is no delivery metric to build one from, stated as a gap in
[`docs/operations/reliability.mdx`](./docs/operations/reliability.mdx) rather
than invented. The transactional outbox remains unconsumed by design — no
external side effect depends on it yet — and is called out the same way
rather than built into a speculative dispatcher. Capacity guidance is
starting points for host sizing, not a load-tested ceiling: no throughput
benchmark ships with the repository.

### Documentation and community

- [x] End-to-end installation, configuration, migration, upgrade, backup, and troubleshooting guides
- [x] Bot customer guide, admin operator guide, and customer web guide
- [x] Integration guides for Remnawave, Telegram, and every supported payment provider
- [x] AI-provider, local-model, MCP client/server, privacy, cost-control, and troubleshooting guides
- [x] Public API reference and extension policy
- [x] Contributor setup, architecture decisions, testing strategy, and release process
- [x] Issue templates, feature-request process, support boundaries, and code of conduct

### Definition of 1.0

- [x] A clean installation can be completed from public documentation alone
- [x] Telegram bot, admin panel, and customer panel cover their complete required journeys
- [x] Real payments and Remnawave fulfillment are idempotent and recoverable
- [x] Roles prevent unauthorized operator access and every sensitive operation is audited
- [x] Backup restore and supported-version upgrade have been exercised successfully —
      the upgrade half is exercised on every change by CI's `migrations` matrix
      against a real PostgreSQL, from the empty database through every prefix of
      the migration history to head. The restore half was a documented drill that
      nothing in the repository ran; it is now `tools/restore-drill.sh`, and it
      has been run against a real installation: a 455 KB encrypted backup
      decrypted through `worker --decrypt-backup`, restored into a throwaway
      PostgreSQL 18.4 instance, every table matched, and the sealed OIDC client
      secret opened under `APP_DATA_ENCRYPTION_KEY` while a wrong key was refused
- [x] CI, security, migration, documentation, accessibility, and end-to-end gates are green —
      every job on `main` passes: Go, Web, Integration (Testcontainers), the
      Atlas migration matrix, Compose and reverse proxies, Mintlify docs, and
      End-to-end (Playwright). Getting there took four defects out of the box
      the phase above had already ticked, and none of them was visible from a
      development machine: see [What the green run cost](#what-the-green-run-cost)

### What v1.0 delivered

v1.0 changed almost no behaviour, which is what a release-engineering phase
should look like. It changed what somebody outside this repository can find out.

**The decisions are written down.** No decision record existed,
although `AGENTS.md` and the documentation guide had both required it since v0.1
and two changes — a message broker, a dependency-injection framework — were
already gated on an accepted record. Six records now cover the choices that
constrain everything else: one module with three entrypoints, PostgreSQL as the
only durable store, the OpenAPI file as the contract, the Remnawave boundary, one
web application for both audiences, and AI that is optional and never acts alone.
Each carries the alternative that was rejected, what the choice costs, and the
observable condition that would make reopening it correct — because the expensive
mistakes are the ones nobody remembers arguing about, reversed by a contributor
who had no way of knowing there was a reason.

**The public surface is bounded.** `docs/integrations/extending.mdx` says which
surfaces carry the version promise — `/v1/admin`, the contract, the webhook and
catalogue routes, the probes, the environment variables, the published images —
and which do not. `/v1/panel` and `/v1/account` are named as internal, and every
Go package lives under `internal/` so the compiler enforces that nothing outside
the module can import one. The four things an operator can genuinely plug in
without a contribution are enumerated, and so is the list of what is deliberately
not extensible, starting with the plugin runtime that is absent rather than
overdue.

**The release is a process rather than a habit.** `docs/contributing/releases.mdx`
documents what turns a commit into a version, what the release workflow gates
before it builds anything, and the one step that is detection rather than a gate:
the published-image scan runs after the push, because a multi-arch image cannot be
scanned before `build-push-action` sends it. `release-please-config.json` now
names `1.0.0` as the initial version, and `api/openapi.yaml` carries `1.0.0`
instead of the `0.5.0` it had been stale at.

**The AI documentation stopped implying a feature.** The registry, the sealed
credentials, the per-feature routing, and the budgets are real and are now
documented properly, including how to point the OpenAI-compatible adapter at a
model on your own hardware. What is also documented, on the pages a reader
actually lands on, is that no feature invokes a provider in this build. That was
already stated on the documentation index; it is now stated where it is needed.

**The community files answer the questions people arrive with.** Support
boundaries are explicit about what this project is responsible for and what it is
not — Remnawave itself, provider accounts, bespoke deployment, and the legality of
running a VPN service are all named. The feature-request process says what the
four possible answers to a proposal are. `CONTRIBUTING.md` no longer claims that
Testcontainers and Playwright are scheduled for a later milestone; the first has
been running in CI since v0.5 and the second since v0.8.

Two corrections came out of the work rather than into it. The known defect
carried from v0.5 had been fixed in v0.7 and the section describing it was never
updated. And v0.8's AI and MCP boxes describe implemented, tested code that no
process reaches, which is now stated in that phase's own section rather than left
for a reader to discover by grepping for an import.

### What running it found

Five defects, all in the same blind spot. Everything in this repository is
tested from the inside — unit tests, route tests against a mounted router,
Testcontainers against a real database, and a browser suite that stops at the
sign-in screen because the journeys past it needed a seeded operator that three
phases in a row deferred. Nothing had ever started the shipped stack and used the
product through it. All five are invisible from inside and obvious within a
minute of trying.

**The database was not on the volume that was supposed to hold it.** This is the
one to fix first in any existing installation. `compose.yaml` mounted
`postgres-data` at `/var/lib/postgresql/data`, which was `PGDATA` up to
PostgreSQL 17. Version 18 moved it to `/var/lib/postgresql/18/docker` and
declares its `VOLUME` at the parent, so the named volume an operator sees, backs
up, and is told in `docs/operations/deployment.mdx` carries "everything" was
empty, and the database was in an anonymous volume created to satisfy the
declaration. It survives a restart, which is why nothing looked wrong. It does
not survive `docker volume prune`, which removes it as unreferenced, and it is
not what gets copied when somebody migrates hosts by moving named volumes. The
CI `compose` job validates that the file parses and that the images build; it
never asserted where a byte written to the database ends up.

**Neither panel could reach its API from a browser, in any shipped topology.**
The browser client is same-origin by design: `packages/api-client/src/fetcher.ts`
keeps a base URL of `""`, `setBaseUrl` exists and is called from nowhere, and
there is no CORS anywhere in the Go API. Something therefore has to serve `/v1`
on the web origin — and nothing did. Both reverse-proxy examples put the API on a
separate host, and the compose stack puts it on a separate port, so every call
the panel made landed on Next.js, which has no `/v1` route, and returned 404.
`docs/contributing/development.mdx` asserted that the proxy examples provided the
same origin, which is the sentence that made the gap invisible: it was written as
a description of the examples and was never true of them. The panels rendered,
refused anonymous visitors correctly, and passed every accessibility, layout, and
localisation gate while being unable to complete a single request. Sign-in
reported *"Sign-in failed. Try again."* — the message for a rejected credential.
Fixed at the edge in both proxy examples, and in `apps/web/middleware.ts` for
stacks with nothing in front of them, which is what makes the quickstart work.

**The session cookie was named for a guarantee it did not carry.** Both session
cookies and both OIDC flow cookies were named `__Host-…` unconditionally, while
`Secure` followed `APP_ADMIN_COOKIE_SECURE` and `APP_CUSTOMER_COOKIE_SECURE`. A
browser accepts a `__Host-` cookie only with `Secure`, so on the plain-HTTP local
stack — the configuration the quickstart tells operators to use — the cookie was
discarded on arrival. From the server's side this is silent: it sets a cookie and
never sees it again. The prefix now follows the attribute it depends on, so the
production name is unchanged and the documented local stack works.

**Two translations were missing behind the session gate**, where the localisation
gate could not see them: the sidebar rendered `admin.navigation.items.offers`
because the catalogue had a label at `admin.nav.offers` that nothing reads, and
every dashboard metric rendered its own definition key, because those keys were
stored flat with dots in them while next-intl resolves a dot as a nesting
separator.

The gates that would have caught each of them now exist and run:
`apps/web/e2e/api-reachability.spec.ts` asserts the browser can reach the API on
its own origin, `internal/httpapi/cookiename_test.go` asserts the `__Host-`
prefix appears exactly when `Secure` does across all six cookies, and
`apps/web/e2e/operator-journey.spec.ts` signs in for real and sweeps fifteen
panel pages for untranslated keys. The last of those needed CI to run a real API
rather than the web application alone, which is the change that closes the
seeded-operator debt v0.8, v0.9, and v0.10 each carried forward.

The volume defect now has one too. `tools/volume-drill.sh` starts the shipped
stack under a compose project of its own, asserts the running `data_directory`
sits under the named volume's mount and that the container has no anonymous
volumes, then writes a row, recreates the container, and reads it back. The
`compose` job runs it. Against the pre-fix mount it fails, and it prints the
image's own refusal — which is the second thing running it found: on the pinned
`postgres:18.4-alpine`, a mount at the old `/var/lib/postgresql/data` no longer
misplaces the database silently, it stops the container from starting at all. The
defect described above was the quieter form of the same mistake, and the drill
catches both.

The pattern across all five is worth stating plainly, because it will produce the
sixth: a test that builds the system under test out of the same assumptions as
the code cannot see a wrong assumption. Only running the thing an operator
actually runs can.

### What the green run cost

The last unchecked box in this phase was "the gates are green", and it stayed
unchecked because they were not. The browser gate had never once executed since
it was introduced, and four separate defects sat behind it, each hidden by the
one in front.

**The end-to-end job's encryption key was not a key.** Its fallback decoded to
`development-only-32-byte-key-12345` — thirty-four bytes, not thirty-two — so
the API refused its configuration and exited before binding a port. The wait
loop then polled `/livez` for a full minute against a process that was already
gone and reported a refused connection, so the one line naming the fault sat in
a log nobody opened. The loop now notices the process is dead and prints it.

**The job never configured the commerce runtime at all.** With the key accepted,
the API got one step further and refused for the next reason: no
`APP_OPERATOR_TOKEN`, no `APP_REMNAWAVE_URL`, no `APP_REMNAWAVE_TOKEN`. Three
variables the API has always required, in a job that had never reached the point
of needing them.

**The localisation gate could not tell a missing translation from data.** It
searched the page for a dotted key, because that is what next-intl renders for a
missing message. The audit log stores action names in exactly that shape —
`admin.login`, `admin.bootstrap.completed` — so the first run against an API
with rows in it failed on `/admin/audit`, and the page was fully translated.
Every installation with an audit trail would have failed it. A missing message
now renders a marker no content can contain, defined once and read by both the
application and the suite, which also widens the gate from the two namespaces it
named to all of them.

**An integration test had been asserting a pairing browsers cannot produce.** It
built the customer API with insecure cookies and then presented a `__Host-`
cookie. The prefix and the `Secure` attribute travel together, so the API was
reading the unprefixed name, the session was invisible, and
`TestAccountSurfaceAcceptsARealSessionAndEnforcesCSRF` had been failing since
the prefix shipped — in a job whose other failures kept it from being the thing
anybody looked at.

None of the four is exotic. All four were in the space between "the code is
right" and "the thing runs", which is the same space the five defects above came
from, and the reason that space keeps producing them is that it is the only one
no test written from the inside can see.

---

## 🔭 Post-1.0 product gaps

These came from reading Bedolaga Cabinet 1.61's feature list against this
repository on 14 August 2026 — against the routes and the packages rather than
against a README, which is how [`docs/comparison.mdx`](./docs/comparison.mdx)
was assembled and why that page names almost none of them.

Nothing here is scheduled and nothing here blocks a 1.x release. Each item
states what exists today, so what is listed is the difference rather than the
whole capability, and each states the consequence of leaving it open: an item
whose consequence cannot be named does not belong on this list. The candidates
below this section are directions; these are specific absences.

### Branding and presentation

- [x] White-label theming beyond a name — done, and documented at
      [`operations/branding`](./docs/operations/branding.mdx). A `theme` settings
      section carries a palette for both modes over twenty-three tokens, a corner
      and a spacing scale, which modes the installation offers, and its default;
      `branding_assets` carries a logo per mode and a tab icon. It is the first
      installation setting that actually takes effect — both panels read
      `GET /v1/branding` server-side and inline the declarations before first
      paint, so there is no flash of somebody else's brand and no environment
      variable to restart for.

      Three decisions are worth reading rather than inferring. **The status tones
      are not settable**: an installation whose *destructive* is green has not
      been branded, it has been broken, in the one place where being wrong costs
      somebody an action they cannot undo. **Contrast is computed, not trusted** —
      against the resolved palette, so setting only `card` is checked against
      every foreground drawn on a card; below 3:1 refuses the save naming the
      pair, between 3:1 and 4.5:1 saves with a warning, because a brand tone that
      fails AA is a decision an operator is entitled to make and unreadable text
      is not. And **SVG is refused**, because these files are served from the
      origin that holds the session cookie and an SVG can carry a script.

      There is no second implementation of any of it in the browser, including
      the WCAG formula: two would eventually disagree about which palettes are
      allowed, and only the one that refuses saves would be right. What the panel
      computes locally is the preview, by setting the form's values as custom
      properties on one wrapper and letting the browser answer.

      One thing shipped that was not asked for and is the reason the switcher is
      honest: next-themes has no notion of a restricted set, so `forcedTheme`
      alone would have left every theme control on both surfaces rendered,
      pressable, and inert. The allowed set is published to the client and both
      controls disappear instead.
- [x] Operator-editable client application catalogue — done, and documented at
      [`operations/connection-catalogue`](./docs/operations/connection-catalogue.mdx).
      `connect_platforms` and `connect_clients` replace the compiled table;
      `/admin/settings/connect` edits them, and an operator can add a client, add
      a platform, give a client a download address, or write their own setup
      instructions per platform and per language without a release.

      The single source survived the move, and that is the whole point: it used
      to be guaranteed by both surfaces linking one constant and is now
      guaranteed by both reading one query through `internal/connectpg`. Disabling
      a platform removes it from the chat and the browser at once, including from
      a link that names it directly, because the predicate is in the query rather
      than in one caller. An integration test asserts that; another asserts the
      seed reproduces the eleven entries and five platforms the binary carried, in
      the order it recommended them and with the labels the bot's own catalogue
      used, so an upgrade is a no-op for anybody who never opens the screen.

      One field became a security boundary in the move. The import scheme is
      concatenated with the subscription link and rendered as the `href` of an
      anchor on the origin that holds the session cookie, so it is checked against
      a pattern and against a denylist of `javascript`, `data`, `vbscript`, and
      `file` — in Go so an operator gets a message naming the field, and in the
      table so a script writing directly is refused too. A download address is
      https-only for the neighbouring reason.

      Two smaller consequences, both improvements. Platform labels moved out of
      the bot's compiled message catalogue into stored per-locale columns, because
      an operator adding a platform cannot add a translation key. And an
      operator's own instructions replace the generic three steps rather than
      appending to them, on both surfaces.

### Reporting and growth measurement

- [x] Sales reporting over a period an operator chooses — done, at
      `/admin/reports` and documented at
      [`operations/sales-reporting`](./docs/operations/sales-reporting.mdx). The
      dashboard's fixed window stays exactly where it is, for the reason it was
      fixed; this is the screen where it moves. It breaks down by kind of sale —
      which is what separates new purchases from renewals, add-ons, and top-ups —
      by plan version and billing period, and by day, with trial conversion and
      refunds issued, plus a CSV of all of it.

      Building it found the reason the fixed window had been getting away with
      it. Reporting keyed on `orders.updated_at`, which moves on any write, so
      recording a refund in March silently moved a February sale into March and
      the same report over the same closed period returned different numbers
      each time. `orders.paid_at` is written once, on first settlement, and never
      again; an integration test records a refund against a closed period and
      asserts the figures do not move.

      Three smaller decisions are stated on the screen rather than left implied.
      Provider money and wallet credit are never added, because the balance was
      already revenue when it was funded. Refunds are dated by when they were
      issued, not by the sale they reverse. And trial conversion is labelled as
      a cohort measure, because the numerator counts conversions at any later
      time and a period ending today reads low by construction — a figure people
      misread once and then distrust forever.

      Days bucket in the operator's own timezone rather than UTC, with a test
      asserting the same sale lands on the 2nd in UTC and the 3rd in Moscow. The
      route went over the first-load JavaScript budget once recharts and the
      date picker's calendar were both on it, so the chart loads on demand and
      the tables — which carry the same figures and are the accessible
      equivalent the chart needs anyway — arrive first.
- [x] Payment health per provider — done, as the second tab of `/admin/reports`
      and documented on the same page. Settlement, latency, and webhook intake
      per adapter, with a daily series, so an acquirer that starts refusing is a
      number that moved.

      It reports **two** rates, and the distinction is the substance rather than
      a detail: settlement is settled ÷ (settled + failed), the provider's own
      success on payments it was asked to take, and completion adds the customers
      who walked away. Collapsing them into one figure produces a number that
      drops every time a campaign reaches people who were never going to buy,
      which is how a real settlement problem gets lost in noise. Intents still in
      flight are in neither denominator — one created five minutes ago has not
      failed — and the age of the oldest is the stuck-payment queue as a number.

      Two smaller refusals. A rate is absent rather than zero when nothing
      reached a decision, because "nobody paid" and "everybody failed" are
      opposite facts and a quiet adapter reading 0% sends somebody to investigate
      nothing; the sample size travels beside every rate for the same reason.
      And time-to-settle is measured from the settlement event rather than from
      `updated_at`, which a later reconciliation moves — the same class of
      mistake `orders.paid_at` was added to fix.
- [ ] Advertising measurement — no counter integration, no offline-conversion
      upload, and no click identifier carried from a first visit into the order
      that settles later. Payment happens on the backend, sometimes a day after
      the click, so without it no advertising channel can be attributed at all.
      Site-verification meta tags for webmaster tools are absent for the same
      reason: nothing renders them. This is the operator's own analytics, never
      project telemetry, so it stays per-installation, off by default, and inside
      the consent the marketing surfaces already enforce.
- [x] Traffic reporting per node — done, at `/admin/traffic` and documented at
      [`operations/traffic-reporting`](./docs/operations/traffic-reporting.mdx).
      Node saturation sorted by pressure, the fifty heaviest users with the
      customer who holds each one, and a CSV of both. The anomaly signal keeps
      answering the question it answers.

      **Nothing is stored, and that is the design rather than a shortcut.**
      Remnawave owns traffic, nodes, and connections; this repository still has
      no table for a node and no column for a byte a customer used. Both halves
      are read from the panel on each request and no history is kept, because
      keeping one is the first step towards Omniflow having an opinion about
      traffic. The single thing Omniflow contributes is the join from a
      Remnawave user identifier to the customer who holds it.

      `GET /api/nodes` is the fifteenth call in the adapter and the only one
      whose absence is a normal outcome rather than a fault. A panel that does
      not expose it produces a stated absence on the screen — "this panel does
      not expose a node listing" — instead of a page of zeros, because an empty
      node table and "we could not ask" look identical and mean opposite things.
      The decode is lenient for the same reason: a panel that shapes the payload
      differently degrades that one section and leaves the rest of the page
      working. **Confirm it against your own panel**; this is the one upstream
      shape in the adapter that no test written inside this repository can
      verify.

      Two smaller refusals. A node with no limit reports no saturation rather
      than 0%, because it cannot be filling up and zero would sort it among the
      empty ones. And the consumer ranking says how far it scanned whenever it
      did not reach the end of the user base, since a truncated ranking
      presented as a complete one is worse than no ranking.

### Revenue protection

- [ ] Account-sharing detection — device limits come from Remnawave and are the
      only control today. Distinct-address concurrency, with a network-type
      allowance so that a mobile carrier is not mistaken for sharing, is the
      measure that finds one subscription serving a group. Whether Omniflow may
      hold that observation at all is a boundary question that has to be answered
      before it is a feature: Remnawave is authoritative for traffic and
      connections.

### Content and communication

- [x] Information pages an operator can publish — done, at `/admin/content` and
      documented at
      [`operations/information-pages`](./docs/operations/information-pages.mdx).
      `/pages/{slug}` is public, server-rendered, and bilingual, and an operator
      publishes the FAQ, terms, offer, privacy policy, or anything else at an
      address of their own.

      **The address is the identity.** A payment provider approves a URL, so the
      slug is the primary key rather than a field beside a generated one: there
      is no way to change an address while keeping the page, because that would
      break an approval silently. Withdrawing takes the address out of the world
      reversibly; deleting takes the address itself and asks the operator to type
      it.

      **The body is never HTML, and that is the substance rather than an
      implementation note.** These documents are written by an operator and
      served from the origin that holds the session cookie, so rendering them as
      HTML — with or without a sanitiser — would put the page one sanitiser bug
      away from stored cross-site scripting. `internal/infopage` parses the
      source into typed blocks and typed inline runs, and the browser renders
      text nodes and anchors; there is no sanitiser in the path because there is
      nothing to sanitise. The syntax is correspondingly small — headings,
      lists, paragraphs, and https-only links — and an unrecognised construct
      becomes visible literal text rather than a refusal, because an operator
      with a published page they cannot see and no line number is worse off than
      one with a paragraph that reads oddly.

      Server-rendered because the readers who matter are a payment provider's
      reviewer, an application store's reviewer, and a search engine, and none of
      them is guaranteed to run JavaScript. A published page can also be unlisted
      — a stable address with no menu entry — which is exactly what a document
      that exists to satisfy a review needs.
- [ ] Transactional e-mail templates an operator can override —
      `/v1/panel/marketing/templates` covers campaigns. System mail has no
      per-type, per-locale override, no variable reference, no preview against
      real substitutions, and no test send.
- [ ] Notification history and a test notification — `/v1/account/preferences`
      sets what a customer receives and `/preferences/unsubscribe` stops it.
      Neither the customer nor an operator can see what was actually delivered,
      which leaves "I never got it" unanswerable.

### Identity and lifecycle

- [x] Merging two customer accounts — done, on the customer page and documented
      under
      [`commerce/customers-imports`](./docs/commerce/customers-imports.mdx). One
      audited, idempotent operation with a preview that counts everything before
      anything moves, and the direction fixed and stated: the account you are
      looking at is the one being absorbed, because "merge A and B" is ambiguous
      in exactly the way that matters.

      Forty-six tables reference `users`, and the work was deciding which of them
      are a change of owner and which are a decision. Five are decisions.
      Subscription slots are unique per customer, so moved subscriptions are
      renumbered. **The wallet moves as a compensating pair of ledger entries**
      rather than by reassigning rows, because `ledger_entries` is append-only
      and those entries are the evidence for every balance the installation has
      ever reported. A saved cart is cancelled — one may be open per customer. A
      read marker moves only where the target has not read the same post, since
      the pair is a primary key and a merge should not fail over a read receipt.
      And a saved payment method arrives non-default, because one default per
      customer is a unique index.

      Five refusals, listed in the preview and **recomputed when the merge is
      applied** rather than trusted from it — the two are separate requests, and
      a merge authorised by a stale screen goes wrong exactly once. The one worth
      naming: if either account referred the other, the merge is refused, because
      it would make a customer their own referrer and somebody was paid a reward
      for that signup.

      The source is marked `merged` and keeps its rows and its history rather
      than being deleted: support answering "where did my subscription go" needs
      somewhere to land. Both accounts get a lifecycle event, so the merge reads
      from either customer's own history and not only from the operator trail.
- [x] Pausing a subscription — done, on the customer's subscription in the panel
      and documented under
      [`commerce/subscriptions`](./docs/commerce/subscriptions.mdx). Access stops
      through the ordinary fulfillment pipeline and the entitlement's clock stops
      with it, in one transaction; resuming moves `ends_at` forward by exactly
      the elapsed pause and records the same figure in `paused_seconds`, so an
      expiry later than the order paid for is explained by its own columns.

      The properties live where they cannot be forgotten. The guards are `UPDATE`
      predicates, so two operators pressing the button produce one pause and one
      refusal rather than a second pause that resets the first instant. `paused`
      and `paused_at` are paired by a table constraint. And resume is one
      fulfillment operation rather than two, because the new expiry has to reach
      Remnawave before the user is re-enabled — otherwise the user is briefly
      enabled carrying an expiry that has since passed.

      **The reconciler had to learn about it**, and this is the part that would
      have quietly undone the feature. A paused subscription and a disabled one
      are identical from Remnawave's side, so a reconcile a week later sees
      either a disabled user or — once real time walks past the frozen expiry —
      an expired one. Mapping either back would have taken away the days the
      pause exists to preserve, a week after the customer was told they were
      safe. Both now count as agreement; an *active* remote user while locally
      paused is still reported as drift, because that is a customer connecting
      on time nobody is counting.

      There is deliberately no customer-facing pause: it turns a dated
      entitlement into an indefinite one, and letting the holder decide when the
      clock runs makes "thirty days" mean "thirty days of my choosing, forever".
      What the customer does get is an honest state on both surfaces — paused,
      days kept — rather than "disabled, contact support", which would send
      somebody to a ticket queue to be told nothing is wrong.
- [x] Wholesale code batches — done, under `/admin/catalog` and documented in
      [`commerce/catalog-promotions`](./docs/commerce/catalog-promotions.mdx).
      `code_batches` and `access_codes` generate a block against a plan version
      at an agreed price, hand the list over once, and revoke the unredeemed
      remainder with a reason.

      **The codes exist once**, and the screen says so before the operator
      generates a batch rather than after. Only the SHA-256 is stored, so no
      endpoint can produce them again and there is nothing in the database to
      produce them from; the result panel offers a download and a copy and
      confirms before it closes.

      The redemption path is where the design paid off. A batch code and a gift
      code are the same sixteen Crockford characters, so the format moved into
      `internal/accesscode` and the existing claim box accepts either — a
      customer holding a code has no way to know which kind it is, and asking
      them to pick the right form would be asking them to know something only
      the operator does. Single redemption is the `UPDATE` predicate rather than
      a lock, and every refusal is identical, because distinguishing "unknown"
      from "already used" turns the endpoint into an oracle for which codes
      exist.

      One consequence worth stating: a redemption creates a **zero-value order**
      with the operation `code`. `entitlements.order_id` stays NOT NULL, so every
      entitlement is still traceable to a transaction, and the sales report shows
      the redemption with empty money columns instead of inventing revenue on a
      day nobody paid. The wholesale price lives on the batch, where the
      arrangement actually is. Revoking spares redeemed codes, because somebody
      is using each subscription they produced.
- [x] A recorded decision on customer password sign-in — decided against, and
      written down as
      [`decision-0007-no-customer-password`](./docs/architecture/decision-0007-no-customer-password.mdx).
      The reason that settles it is not preference: there is no e-mail transport
      in this installation at all, so a password would be a credential whose
      reset path cannot reach the one person who needs it. The record states the
      cost plainly — an installation with neither Telegram nor an acceptable
      OIDC provider cannot sign anybody in on the web, and no configuration
      produces one — and names the two observable conditions that would reopen
      it. The sign-in section of the customer web guide now points at it, so a
      reader comparing the two route lists finds the choice rather than
      inferring an oversight.

### Panel and customer experience

- [ ] Command palette — the panel is around thirty screens reachable only by
      navigating to them.
- [ ] Live updates — every panel and account surface polls. A ticket reply, a
      settled payment, and a bulk operation's progress all arrive on the next
      request, and `/v1/panel/bulk/{operationID}/items` is a list somebody
      refreshes rather than a stream.
- [ ] Multi-currency with rates — amounts are integer minor units in the order's
      own currency, which is the correct storage and is not the gap. What is
      missing is a rate source and a presentation currency, so an installation
      that sells in one currency cannot quote a price in another.

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
