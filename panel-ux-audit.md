# Omniflow admin panel — UX/UI audit

Walkthrough as an operator (owner of a VPN service running Remnawave + Omniflow).
Recorded live while clicking through every page and button of `http://localhost:3000/admin`.

Legend:
- **BLOCKER** — the job cannot be done from the panel at all.
- **PAIN** — possible, but slow / awkward / error-prone.
- **GAP** — I expected it on this page and it is not there.
- **BUG** — broken behaviour.
- **POLISH** — copy, layout, consistency.

> Test data left behind by this audit: wholesale code batch `audit-test-batch` (3 codes, plan
> `starter` v1), two support replies on ticket `055830fe…`, one test notification, and a
> suspend + reinstate cycle on customer `1780a391…`.

## Fixed since this audit

- **Charts.** The dashboard gained a customer-state ring, an entitlement-state ring, a
  payment-outcome ring, and a sorted backlog bar; Sales gained orders-per-day and revenue-by-plan;
  Payment health gained a settled/failed/abandoned bar per provider; Traffic gained node pressure
  and heaviest consumers. Every one carries its figures as a table, which the chart contract here
  requires.
- **Clickable tiles.** Every dashboard metric with a known destination is now a link.
- **Duplicate "FAILED" labels** in Operations are now "Fulfilment failed" and "Callbacks failed".
- **Audit log** shows who acted and why — the trail always carried both and the table never did.
- **Catalog → new version** starts from the current version instead of an empty form.
- **Finance** rows carry an explicit open control rather than hiding the link on the timestamp.
- **Sales tabs** show a skeleton while their chunk loads instead of rendering nothing.

Still open, and each is a feature rather than a fix: payment-provider configuration, operator
invites and roles, record-level global search, promotion and segment creation, shop products,
the customer import apply step, customer-page actions (grant, wallet, devices, message), and
order refunds.

---

## The ten things I would fix first

1. Global search finds only pages — I cannot look a customer up by @handle, email, or payment id.
2. Payment providers cannot be configured. The table on Settings → Commerce has headers and no
   way to add a row. Nothing can be sold.
3. The Shop module has no "add product" and no "add provider" buttons — only empty states telling
   me to add them.
4. Promotions cannot be created, so personal offers (which require one) are unusable, and
   audience segments cannot be created, so campaigns are unusable.
5. Plans have no price anywhere in the UI, no detail page, and "new version" starts from a blank
   form instead of the current version.
6. A customer page is titled with a raw UUID and offers exactly one action: Suspend (no
   confirmation). I cannot grant a subscription, top up a wallet, or reset devices.
7. Orders have no detail view and no refund.
8. New operators cannot be invited; roles cannot be changed.
9. The audit log has no "who did it" column.
10. Bulk actions want pasted subscription IDs that the panel never shows anywhere.

---

## Cross-cutting (whole panel)

- **BLOCKER — global search finds only pages, not records.** `⌘K` says "Type a page or a
  command…", but typing `telegram` returns "Nothing matched." The most frequent operator action is
  "find this customer by @handle / Telegram ID / email / payment id". It should search customers,
  orders, payments, tickets, promo codes — and it should expose real *commands* ("credit wallet",
  "issue gift", "open ticket"), which today do not exist at all.
- **PAIN — tabs never change the URL.** Customer tabs (Subscriptions/Orders/Wallet/…), Finance
  tabs, Sales tabs, Catalog tabs, Shop tabs, System tabs: all local state. I cannot link a
  colleague to "this customer's wallet", and F5 always throws me back to the first tab. Support is
  the one exception (`?ticketId=…`) — do what Support does everywhere.
- **PAIN — no filter state in the URL either.** Filtered lists are not shareable or bookmarkable,
  and Back does not restore them.
- **PAIN — timezone story is inconsistent.** Dashboard says "all times UTC"; Sales says "days are
  bucketed in Europe/Moscow, which is this browser's timezone"; tables render `8/15/2026,
  1:16:37 AM` in US locale. Three conventions on three pages. Pick one **operator-level timezone
  setting**, show it once in the header, render every timestamp in it.
- **POLISH — dates are US-format with 12-hour AM/PM** next to `RUB` amounts on the same row. Use
  `2026-08-15 01:16` or the operator's locale.
- **GAP — no relative time anywhere.** "Registered 8/15/2026, 12:42:19 AM" — I want "2 hours ago"
  with the absolute time on hover. Same for tickets, orders, incidents, backups.
- **GAP — panel is English only.** The account menu has Light/Dark/System but no language. The bot
  has locales; the panel should too (RU at minimum, given the target audience).
- **PAIN — nothing is click-to-copy.** UUIDs are truncated in tables (`1780a391`) and shown in full
  as page titles, but there is no copy affordance anywhere.
- **GAP — no notification centre / bell for the operator.** Incidents, stuck payments, failed
  fulfilment, drift — I only learn about them by walking to the right page.
- **PAIN — session expired mid-walkthrough with no warning** and dumped me to the login screen.
  `?next=` preserved the destination (good), but unsaved form content would have been lost. Add a
  session-expiry warning and inline re-auth. Also the first click on **Sign in did nothing visible
  and the second one worked** — the button needs a pending state and an error message on failure.
- **BUG — the 404 page is the bare Next.js default** (black page, "This page could not be found"),
  with no sidebar, no branding, no way back into the panel.
- **PAIN — inconsistent form controls.** Sales/Catalog/Marketing use shadcn Combobox + a custom
  calendar; Customers bulk actions, Catalog "new version", Support ticket controls and Content use
  raw native `<select>`; Brand settings uses a **free-text field for Timezone and Default locale**;
  Operations settings uses free-text for "Quiet hours start/end"; AI settings uses raw radio
  buttons and free-text for Provider/Model. Per the repo's own rule: no native selects, no
  free-text where a picker belongs.
- **PAIN — "minor units" leak into every operator-facing field** (prices `RUB=49900`, top-up
  limits `10000`, referral rewards, wholesale price, risk thresholds). Operators think in
  `499.00 ₽`. Format the input, store the integer.
- **PAIN — the disabled-button pattern is used everywhere with no explanation.** Preview, Publish
  version, Create offer, Create draft, Save are all greyed until some unnamed field is filled, with
  no required markers and no hint about which field is missing.
- **GAP — no keyboard shortcuts beyond `⌘K`**, and on mobile the search button disappears entirely.

---

## Dashboard (`/admin`)

- **POLISH — the intro copy is stale and wrong.** "This release delivers the panel shell, operator
  accounts, and the audit trail. Customer, finance, and catalog surfaces arrive in the next
  versions." All of those surfaces exist and are in the sidebar.
- **GAP — the reporting window is text, not a control.** "window 30d" is baked in. I want
  24h / 7d / 30d / 90d / custom here, like Sales has.
- **GAP — no refresh / auto-refresh.** "Read 8/15/2026, 1:09:47 AM" is stale on arrival; F5 is the
  only way to update.
- **BLOCKER-ish — no KPI tile is clickable.** "SUSPENDED", "STUCK", "RENEWALS DUE", "OPEN DRIFT",
  "OUTBOX BACKLOG" are exactly the numbers I want to drill into. Every tile should link to the
  filtered list behind it.
- **GAP — no charts at all.** The whole dashboard is number tiles. Morning-coffee view wants a
  revenue line, a signups line, and an active-subscriptions line.
- **GAP — no MRR / ARPU / churn / LTV anywhere in the product.** For a subscription business these
  are the headline numbers and the panel computes none of them.
- **PAIN — two tiles in "Operations" are both labelled "FAILED"** (fulfilment operations vs
  provider callbacks), side by side. Qualify the labels.
- **PAIN — "Recent incidents" rows are inert.** No detail, no acknowledge, no impact, no history.
- **GAP — no node / server health.** As the VPN operator I want Remnawave node status (online,
  load, last seen) here. Today Remnawave only appears as an incident after it broke.
- **POLISH — trend deltas are inconsistent.** Some tiles show "+1 vs previous period" or
  "unchanged", most show nothing.

---

## Customers (`/admin/customers`)

### List

- **BLOCKER — the list has no operationally useful columns.** Customer (truncated UUID) / Status /
  Telegram / Locale / Registered. Missing: subscription state + expiry, wallet balance, lifetime
  spend, traffic used, last activity, referral source. I cannot answer "who is about to expire" or
  "who are my top spenders" from this screen.
- **PAIN — search is exact-identifier only** ("Customer ID, Telegram ID, or Remnawave username").
  No email, no @username, no order id, no partial match.
- **PAIN — pagination gives no totals.** "1 shown · page 1" with Previous/Next. No record count, no
  page-size selector, no last page. At 10k customers this is unusable.
- **GAP — no sorting on any column.**
- **GAP — no saved views / segments.** A "Segment" filter exists but I cannot define a segment, and
  there is no preset for "expiring in 7 days", "trial, never paid", "spent > X".
- **GAP — no "create customer" action** for a manual/off-platform sale.
- **GAP — no row selection**, which makes the bulk-action panel below unusable.
- **GAP — no CSV export of the *filtered* list** (only the full-installation export).

### Bulk actions

- **BLOCKER — bulk actions want a pasted list of *Subscription IDs*, and nothing in the panel ever
  shows a subscription ID.** No row selection, no "apply to current filter", no "apply to segment".
  This is a feature I physically cannot use with the data the UI gives me.
- **PAIN — only four actions** (extend / enable / disable subscriptions, credit wallets). Missing:
  bulk suspend/delete customers, bulk reset devices, bulk revoke subscription links, bulk tag.
- **PAIN — Preview is silently disabled** until Reason is filled; Reason has no required marker.
- **PAIN — the validation error is useless.** "Those details are not valid, or a reason is
  required" does not say *which*, and does not say which pasted identifier is bad. With 200 IDs
  this is a dead end. Return per-line validation.
- **PAIN — Action is a raw native `<select>`**, unlike the comboboxes used elsewhere.

### Import / export

- **BLOCKER — there is no way to *start* an import.** The card explains "Customer imports are
  previewed before they are applied", and a job is listed (`32595866d · Ready · Resumable ·
  473 records · 0 invalid`), but there is no file-upload control anywhere on the page.
- **BLOCKER — the import job row is inert.** It says "Ready", but I cannot open the preview, see
  the 473 rows, see conflicts, **apply** it, or cancel it.
- **GAP — no import history and no downloadable error report** for invalid rows.

### Customer detail (`/admin/customers/{id}`)

- **BLOCKER — the page is titled with a raw UUID.** No name, no @handle, no email. I cannot tell
  who this is, and neither can whoever I send the link to. The headline should be the human
  identity; the UUID belongs in a small copyable meta line.
- **BLOCKER — I cannot do the everyday operator jobs from here.** Missing entirely:
  - grant / extend / cancel a subscription (gift a month, give a trial, comp an outage);
  - top up or debit the wallet, or even *see* the balance — the Wallet tab shows movements only and
    the header shows no balance;
  - reset devices / HWID, reset or rotate the subscription link;
  - message this customer (only "Send a test" exists);
  - edit anything — locale, timezone, notes, tags;
  - delete / anonymise the account (GDPR request);
  - open this user in Remnawave, or see which nodes/squads they are on.
- **BUG/RISK — Suspend fires with no confirmation.** The most prominent element on the page is a red
  Suspend button with the Reason field always open. One click and the customer is suspended, no
  "are you sure", no undo. The reverse action ("Reinstate") is rendered in the same destructive red.
  Catalog's "Revoke remainder" dialog is the right pattern — reuse it here.
- **BUG — Suspend with an empty Reason does nothing at all** — no error, no toast, no field focus.
- **BUG — suspend / reinstate never appear in the customer Timeline.** I suspended and reinstated
  this customer; the Timeline still shows only `customer.notification.tested`. (The audit log *does*
  record them — so the timeline is simply not reading the right events.) Status changes are exactly
  what a timeline is for.
- **POLISH — Timeline shows raw event keys** (`customer.notification.tested`) and actor "System".
  Show "Test notification sent by Local Preview", raw key on hover.
- **RISK — "Merge into another account" sits permanently on the page**, at the same visual weight as
  the content, on *every* tab. Its own copy says "nothing can be undone afterwards". It belongs in a
  collapsed danger zone behind a typed confirmation, and it needs a customer *picker* with search
  and a preview of what moves — not "paste an identifier".
- **PAIN — the Overview counters are not links.** "ORDERS 1", "OPEN TICKETS 1", "REFERRALS 0" should
  jump to the matching tab.
- **GAP — no last-seen / last activity, no total spend, no wallet balance in the header.**
- **PAIN — breadcrumb stays "Dashboard › Customers"** — it never names the customer.
- **PAIN — the Orders tab row is not clickable** and shows "topup / pending / RUB 0.00" with no
  amount requested, no provider, no payment id.
- **POLISH — the "Send a test" toast is clipped at the bottom-right of the viewport.**

---

## Finance (`/admin/finance`)

- **BLOCKER — orders have no detail view.** Rows are not clickable. No line items, no provider
  payment id, no callback history. Reconciliation with YooKassa / crypto / Stars is impossible.
- **BLOCKER — no refund, no manual "mark as paid", no cancel.** Not a single action on this page.
  Refunds are a daily support reality.
- **GAP — no date range filter.** Only State and Operation. "Yesterday's orders" is not expressible.
- **GAP — no search** by order id, customer, or provider payment id.
- **GAP — no provider/method column and no provider filter.**
- **GAP — no totals.** No sum of paid / refunded for the filter, no per-currency subtotals.
- **PAIN — the customer cell is a truncated UUID and not a link.**
- **GAP — "Paid RUB 0.00" on a pending topup** with no "amount requested" column: the row carries no
  information about what the customer was actually asked to pay.
- **GAP — no wallet ledger view** (all credits/debits across customers), no payouts, no
  provider-fee / net-revenue view.

---

## Sales (`/admin/reports`)

- **BUG — the "Payment health" and "Channels" tabs render completely blank.** No content, no empty
  state, no error, no console error — the page just ends below the tab strip.
- **POLISH — the nav item is "Sales" but the route is `/admin/reports`.**
- **GAP — no charts, despite the copy promising "by day".** Everything is a number.
- **GAP — the reporting timezone is hard-wired to the browser** ("Europe/Moscow, which is this
  browser's timezone"). A report that silently changes meaning depending on who opens it is a
  reporting bug. Make it an explicit setting.
- **PAIN — the date picker opens on the wrong month** (From = 16 July, picker opened on August) and
  has no "today"/"now" shortcut and no clear.
- **BUG — the date popover swallows clicks meant for the tabs underneath.** Clicking "Payment
  health" while the picker is open silently changes the From date instead. Escape does not close it.
- **PAIN — the popover overlaps the Export CSV button.**
- **GAP — no comparison to the previous period**, no cohort/retention, no per-plan revenue ranking,
  no refund rate.

---

## Traffic (`/admin/traffic`)

- **BUG — the page renders an empty grey box.** No table, no headers, no empty state, no filters —
  just the explanatory paragraph and an "Export CSV" button above a blank card.
- **GAP — no per-customer or per-node breakdown, no sorting, no "top talkers" list**, which is the
  only reason to open a traffic page.

---

## Catalog (`/admin/catalog`)

- **BLOCKER — plans have no price anywhere in the list.** Columns are Code / Kind / Version / Order
  lines / Max per customer / Visible. As the owner I cannot see what any plan costs.
- **BLOCKER — plan rows are not clickable and there is no plan detail page.** I cannot see the
  current version's duration, traffic cap, device cap, squads, or prices at all.
- **BLOCKER — "New version" opens a completely blank form.** Nothing is prefilled from the current
  version, and the current version's values are not visible anywhere else, so to change *only the
  price* I have to retype the whole plan definition from memory. Prefill from the active version and
  show a diff before publishing.
- **BLOCKER — there is no "New plan" button.** I can only version existing plans. No archive/delete
  either.
- **BLOCKER — Promotions tab has an empty state and no "New promotion" button.** Discount codes
  cannot be created, which also makes Personal offers unusable (they require a promotion).
- **PAIN — Prices are a comma-separated string in minor units** (`RUB=49900, USD=599`). One typo
  changes what customers pay. Use per-currency rows with formatted amount inputs.
- **PAIN — Squad IDs are a comma-separated list of raw Remnawave UUIDs typed by hand.** There is no
  picker fetched from Remnawave. I have to alt-tab to Remnawave and copy UUIDs.
- **PAIN — six native `<select>`s in the version form** (Billing period, Squad selection, Upgrade,
  Downgrade, Cancellation, Trial eligibility).
- **PAIN — the version form expands inline inside the table**, pushing the other plan rows around.
  A drawer or dialog would be steadier.
- **GAP — no customer-facing plan name/description in the form**, and no preview of what the
  customer sees.
- **PAIN — "Traffic (GB): leave empty for unlimited. Zero is not the same thing."** Good copy, but a
  free-text field where empty and `0` mean opposite things is a trap. Use an explicit
  "Unlimited / Limit to __" control.
- **GOOD — wholesale code batches are the best flow in the panel.** One-time reveal with
  Download / Copy all, a batch table with Unredeemed / Redeemed / Revoked / Per code, and a
  "Revoke remainder" **confirmation dialog with a reason field and honest consequence copy**. This
  is the pattern every destructive action in the panel should copy.
- **PAIN — the Plan dropdown in "New batch" renders translucent**, with the label behind it showing
  through the options (the Version dropdown next to it is solid).
- **PAIN — Currency in "New batch" is a free-text input** (`USD`).
- **GAP — batch rows are not clickable**: I cannot see which individual codes were redeemed by whom.

---

## Shop (`/admin/shop`)

- **BLOCKER — the whole module is unusable from the panel.** Products tab says "Add a product and a
  pricing rule before opening the shop"; Providers tab says "Configure a digital-goods provider" —
  and **neither tab has a button to do it.** Empty states that give instructions but no action.
- **POLISH — nav says "Shop", the page title says "Digital goods".**

---

## Gifts (`/admin/gifts`)

- **BUG — an orphaned "Reason for revoking (required)" text field floats at the top of an empty
  list**, with nothing selected and no button next to it.
- **GAP — no way to create a gift.** The customer page cannot gift a subscription either, so
  "give this customer a free month" has no home in the panel.
- **POLISH — a lone "All" filter pill with nothing to filter between.**

---

## Offers (`/admin/offers`)

- **BLOCKER by dependency — creating an offer requires a Promotion, and promotions cannot be
  created** (see Catalog). The Promotion dropdown opens as an empty sliver with no "nothing here
  yet" message.
- **PAIN — "Customer ID" is a raw UUID paste field.** No search picker. To make an offer for a
  customer I have to find them in Customers and copy the UUID out of the URL.
- **GAP — no entry point from the customer page** ("make this customer an offer").
- **GAP — EN and RU are hard-coded**; an installation with other locales has no path. (The
  both-languages-required warning itself is good copy.)
- **GAP — no preview of the card the customer will see.**

---

## Support (`/admin/support`)

- **GOOD — the only page that puts state in the URL** (`?ticketId=…`).
- **GAP — no customer context beside the ticket.** The customer id in the corner is not even a link.
  When answering a ticket I need their plan, expiry, wallet balance, last order and last payment
  right there — that is the whole job.
- **PAIN — the message thread is a ~150px scroll box** inside the card, with its own scrollbar.
  Long threads are painful to read.
- **BUG/PAIN — after sending a reply the thread does not scroll to it.** The composer clears and
  nothing visibly happens, so it looks like the send failed. (Both of my replies had in fact sent.)
- **GAP — no search in tickets**, no filter by assignee, no date filter.
- **PAIN — "Release" is unlabelled in intent and there is no "Assign to…"** control.
- **GAP — the page promises "how close it is to what the queue promised" but no SLA/due time is
  shown on the ticket itself** (only aggregate medians in Workload).
- **PAIN — four native `<select>`s** (queue, priority, status, tag) plus "Insert a canned reply…".
- **PAIN — "Merge into ticket ID" is a free-text paste field.**
- **GAP — no attachments, no per-message delivery detail, no undo/delete on a sent reply.**
- **BUG — the Workload tab renders an empty grey placeholder box** below the metric tiles (the
  per-operator breakdown, presumably) with no empty state.
- **POLISH — `?ticketId=` stays in the URL after switching to the Workload tab.**

---

## Content (`/admin/content`)

- **GAP — "Content" only contains information pages.** As an operator, "content" means my bot's
  texts — and those live in Settings → Message wording, where nobody will look for them. Either
  merge them here or cross-link.
- **GAP — no preview / no rendered view** for the restricted markdown body, on a page whose whole
  purpose is publishing legal copy.
- **GAP — no draft/publish control in the new-page form**, although the copy explains that drafts
  404 until published.
- **GAP — EN/RU tabs with no indicator of which language is filled in.**

---

## Marketing (`/admin/marketing`)

- **BLOCKER — campaigns require an Audience segment and a Template, and neither can be created.**
  The "Audience segments" section is a heading and a sentence — no list, no "New segment" button.
  Both dropdowns open as empty slivers.
- **GAP — no one-off broadcast.** "Send this message to everyone whose plan expires this week" is
  the single most-used marketing action for a Telegram VPN service and it has no path.
- **GAP — the suppression list is read-only.** "Nobody is suppressed", no add, no remove, no import.
- **PAIN — referral rewards in minor units** with a static `RUB` badge; no tiered rewards, no
  per-plan reward, no reward-in-days option.
- **PAIN — referral stats (Attributed / Qualified / Rejected / Paid out) are not clickable.**

---

## Risk (`/admin/risk`)

- **GAP — blocklists can only be remote URLs.** There is no local/manual blocklist: "block this
  Telegram ID", "block this email domain" has no home. That is the day-to-day need.
- **PAIN — four separate "Save thresholds" buttons** on one Thresholds screen.
- **PAIN — thresholds are in minor units and have no currency selector.**
- **GOOD — the banner "Nothing on this page changes a customer" is exactly the right kind of
  boundary copy.**

---

## Operators (`/admin/operators`)

- **BLOCKER — there is no "Invite operator" / "Add operator" button.** I cannot bring a support
  agent onto the panel at all.
- **BLOCKER — roles cannot be changed and there is no role/permission view.** Both accounts are
  "Owner"; there is no way to create a support-only or finance-only role, and no permission matrix
  anywhere.
- **GAP — the only action is Suspend.** No remove, no password reset, no force-sign-out of that
  operator's sessions, no resend invite.
- **GAP — "Two-factor: Not set up" is shown but cannot be required per operator** from here
  (the installation-wide toggle is buried in Settings → Brand and security).
- **GAP — no last-login column**, which is the first thing you check on a stale account.
- **PAIN — rows are not clickable; there is no operator detail page.**

---

## Audit log (`/admin/audit`)

- **BLOCKER — there is no Actor column.** Columns are When / Action / Category / Outcome / Target.
  In a multi-operator installation I cannot answer "who suspended this customer" — which is the
  entire point of an audit trail.
- **RISK — the Target column renders what looks like a secret**:
  `ai_provider:sk-1a8a220b581d4a05a8b822310bb6e4ff`. If the provider record is keyed by its API key,
  the key (or its prefix) is being written into an append-only, CSV-exportable log. Worth checking.
- **GAP — the reason text required by every destructive action is not shown here.** I am forced to
  type a reason and then it never surfaces.
- **GAP — no date range filter** (only Category / Outcome / Action).
- **PAIN — "Action" is a free-text field** with placeholder `admin.login`, so I have to know the
  exact machine name. Make it a combobox of known actions.
- **PAIN — no row detail / no before-and-after diff** for configuration changes.
- **PAIN — Target UUIDs are truncated and not links** to the record they name.
- **PAIN — repeated identical entries are not collapsed** (five `ai.feature.configured` inside three
  seconds, three `panel.support_ticket.prioritised` inside three seconds).

---

## System (`/admin/system`)

- **GOOD — the best-explained page in the panel.** Health/Failed jobs/Webhooks/Drift/Outbox with
  honest empty states, live dependency probes with latency, and maintenance mode with a reason and
  bilingual customer notice.
- **GAP — "Providers: No providers are configured" with no link or button to configure them.**
- **PAIN — the "Diagnostics bundle" card is duplicated on all five tabs.**
- **GAP — no version number anywhere in the panel** (it only appears inside the telemetry payload
  preview on another page).
- **GAP — no per-node Remnawave view** (nodes online, load, last seen, restart).

---

## Settings

### Hub (`/admin/settings`)
- **GOOD — grouped "by the question you arrived with"; genuinely easy to scan.**
- **PAIN — "AI and MCP" appears both in the sidebar and in this hub**, pointing at the same page.
- **PAIN — breadcrumbs never go deeper than "Dashboard › Settings"**, on any sub-page, and there is
  no "back to Settings" link.

### Commerce (`/admin/settings/commerce`)
- **BLOCKER — the "Payment providers" table has headers (Provider / Enabled / Credentials /
  Connection / Webhook / Recurring), no rows, and no "Add provider" button.** Connecting YooKassa /
  CryptoBot / Telegram Stars / Tribute is the first thing an operator does and it cannot be done.
- **PAIN — top-up limits and preset amounts in minor units** (`10000, 30000, 50000, 100000`).
- **GAP — currency is a static `RUB` badge**; no multi-currency configuration here.

### Integrations (`/admin/settings/integrations`)
- **BLOCKER-ish — no "Test connection" for Remnawave and no "getMe" / "Set webhook" for the
  Telegram bot.** I paste a URL, a token and a secret and have no way to know whether any of it
  works. (The AI settings page *does* have "Test connection" — do the same here.)
- **GAP — "Required channels" configures re-verify interval and grace period but there is nowhere to
  list the actual required channels.**
- **PAIN — the Remnawave base URL field is empty while System reports remnawave "Up"**, so it is
  unclear whether the env or the panel value is in force. Show the effective value and its source.
- **PAIN — four independent Save buttons, no unsaved-changes guard.**
- **PAIN — "Compatibility confirmed" is an unexplained toggle** with no compatibility result shown.

### Operations (`/admin/settings/operations`)
- **BUG — the browser password manager autofills "Backup directory" with the operator's email and
  "Backup encryption key" with a saved password.** One careless Save writes
  `preview@omniflow.local` as the backup directory and rotates the encryption key. These fields need
  `autocomplete="off"` / non-credential-looking names.
- **BLOCKER-ish — backups can be listed but not acted on.** No "Back up now", no download, no
  restore, no verify. "A backup nobody has ever restored is a backup nobody knows works" is written
  on the page, and then there is no restore button.
- **PAIN — "Scheduled backups" is off while four backups of kind "scheduled" exist.** Same
  env-vs-panel ambiguity as Remnawave.
- **PAIN — "Quiet hours start/end" are free-text fields.** Use a time picker component.
- **GAP — notification thresholds are all blank with no defaults shown**, so I cannot tell what
  behaviour I get if I leave them empty.

### Brand and security (`/admin/settings/brand`)
- **PAIN — "Default locale" and "Timezone" are free-text inputs.** These must be pickers; a typo in
  Timezone silently changes every report in the panel.
- **GAP — no logo/favicon upload** on a page called "Branding and contact".
- **GOOD — Appearance (corners/spacing/modes offered/default mode) is a genuinely nice touch.**

### Message wording (`/admin/settings/notices`)
- **GOOD — the notice list, the Telegram-markup editor, the placeholder reference, Preview, and
  "Send to the operator group" are exactly right.**
- **GAP — only nine notices, all subscription-lifecycle.** No welcome/onboarding, payment received,
  referral reward earned, gift received, trial started, ticket-replied.
- **GAP — the cadence is hard-coded** ("Sent 7, 3, and 1 days before a subscription expires"). Let
  me change the reminder schedule.
- **GAP — no "reset to shipped wording" button** once I have overridden a notice.

### Connection guidance (`/admin/settings/connect`)
- **GOOD — per-platform apps with import schemes and per-language instructions; well thought out.**
- **PAIN — a Save button per row** (five platforms, then one per application) and ordering by typing
  numbers into an "Order" field instead of dragging.

### Customer sign-in (`/admin/settings/sign-in`)
- **GOOD — the model page in this panel: presets (Google/Yandex/Discord/Empty), clear per-field
  help, honest warnings, a red Remove.** Every other "add a thing" screen should look like this.
- **POLISH — it is the only settings page with no `<h1>`**; it starts straight at a card.

### Measurement (`/admin/settings/analytics`)
- **GAP — the description promises "the click identifier that connects an advertisement to the
  order it produced" and the page contains only counters and site-verification tags.**

### AI and MCP (`/admin/settings/ai`)
- **GOOD — "Test connection" with the model and latency reported, "Rotate key", and per-provider
  data-handling flags (zero retention / may train).**
- **PAIN — first run is a wall of red.** Eight features, each with its own duplicated
  Provider/Model/Retention form and its own red "No provider and model chosen" banner. Set a
  default provider/model once, let features override.
- **PAIN — Provider and Model are free-text inputs.** Provider should be a picker of my configured
  providers; Model should be fetched from the provider.
- **PAIN — raw radio buttons** for provider kind, inconsistent with the rest of the design system.
- **BUG/GAP — "Usage limits" has a Window column in the table but no Window input in the add form**,
  so a limit cannot be given the window the table wants to display.
- **BLOCKER-ish — "MCP servers: No MCP server is registered" with no "Add MCP server" button.**

---

## Security (`/admin/security`)

- **GOOD — passkeys, TOTP, password change, and a live session list with per-session sign-out.**
- **PAIN — sessions are labelled with the raw User-Agent string.** Render "Chrome 151 · Windows"
  with the UA on hover, and show location/ASN if you have it.
- **PAIN — every sign-in creates a new session and none are reused**, so the list fills with
  identical rows from the same browser (eight after one afternoon).
- **GAP — changing a password does not ask for the second factor**, and the warning "This signs you
  out everywhere else" is a footnote next to the button rather than a confirmation.

---

## Patterns worth spreading

These already exist somewhere in the panel and should be applied everywhere:

1. **Catalog → "Revoke remainder" dialog** — modal, plain-language consequences, required reason,
   destructive styling only on the destructive verb. Use for Suspend, Merge, Remove operator,
   Revoke codes, Restore backup.
2. **Catalog → one-time code reveal** with Download / Copy all and "this is the only copy".
3. **AI settings → "Test connection"** with the concrete result ("deepseek-v4-pro answered in
   1407 ms"). Owed to Remnawave, the Telegram bot, and every payment provider.
4. **Settings → Customer sign-in** — presets + per-field help + Remove. The template for every
   "add a provider / product / segment / promotion" screen the panel is missing.
5. **Support → `?ticketId=` in the URL.** Every tab and filter in the panel should do this.
6. **System → honest empty states** ("Outbox is drained. Every domain event has been published.").
   Contrast with the blank grey boxes on Traffic, Sales → Payment health/Channels, and Support →
   Workload.
