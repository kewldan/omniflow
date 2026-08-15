# Omniflow customer web panel — UX/UI audit

Walkthrough as a subscriber of a VPN service running on Omniflow, against `http://localhost:3000`.
Every page of the customer app was opened and every control pressed, including a full purchase
through to a pending order.

Legend: **BLOCKER** / **PAIN** / **GAP** / **BUG** / **POLISH** (as in `panel-ux-audit.md`).

> Test data left behind: order `58889c2a…` (Starter, ₽199, awaiting manual bank transfer) and the
> subscription `3dc58db3…` it created, plus two spent magic-link rows. Two unconsumed magic-link
> rows for customer `1780a391…` may still be pending in `customer_magic_links`.

## Fixed since this audit

- Magic-link 404 — the issuer now points at `/v1/account/auth/link`, and the web application also
  serves `/account/sign-in/link` so links already delivered still work.
- Sign-in dead end — a real "Sign in through the bot" button to `t.me/<bot>?start=login`, and the
  page detects a widget that never loads and says so in its own words.
- Desktop layout — a persistent sidebar with every destination, the account identifier, and a
  sign-out control above `md`; the phone layout is untouched.
- Store buttons — the catalogue now offers a renewal on the plan the customer holds, an upgrade
  only to something dearer, a downgrade only to something cheaper, and marks the current plan.
- `/account/devices` no longer reports "something went wrong" for an unprovisioned subscription.
- The subscription page shows its state, expiry, days left, and device count.
- "Load more" is gone when there is no further page (the API stopped publishing a cursor for a
  short page).
- A sole payment method is preselected at checkout and on the wallet top-up.
- The wallet toggle is hidden at a zero balance; the balance card no longer prints one number twice.
- Sign out exists, on the account page and in the desktop sidebar.
- Theme offers System; the two language settings no longer contradict each other.
- The digital-goods card is hidden when nothing is on sale; the empty dashboard offers the store.
- "Step 0 of 3" reads as "0 of 3 done"; the manual-transfer card no longer says "nothing to do" and
  offers a way to ask for the details.

Still open: operator-configurable transfer instructions for the manual provider, a public
storefront and pricing without a session, and Terms/Privacy links on the landing page.

---

## The ten things I would fix first

1. Nobody can sign in: the Telegram widget shows Telegram's own "Bot domain invalid" error, and the
   bot's magic link points at a route that does not exist (404).
2. A prospective customer cannot see plans or prices — the whole store is behind the sign-in, and
   the landing page sells nothing.
3. On a desktop browser the app is a 410px mobile column with a floating tab bar, in a black void.
4. The only payment method available is "bank transfer, approved by an operator" — and the customer
   is never told where to transfer the money.
5. `/account/devices` renders "Something went wrong" whenever the subscription is not yet
   provisioned (the API answers 409 and the page treats it as a crash).
6. The subscription page never shows the access link, only a red "issue a new one" — and shows no
   status, no expiry, no plan.
7. The store shows Extend / Upgrade / Move down on **every** plan, including the one you are on.
8. Two different language settings contradict each other across two screens.
9. There is no plain "sign out" — only "sign out everywhere", buried two levels deep.
10. Nothing on any screen tells the customer who they are signed in as.

---

## Sign-in and the public surface

### Landing page (`/`)

- **BLOCKER — this is not a storefront.** A VPN service's front door shows plans, prices, what you
  get, and a "buy" button. This page is a single card in the middle of a black void with one link
  into a sign-in wall. A prospective customer has nothing to read and nothing to buy.
- **BLOCKER — plans and prices are behind the sign-in.** `/account/store` redirects to "Sign in to
  continue". Nobody can find out what the service costs without already having an account.
- **POLISH — the eyebrow reads "OMNIFLOW CUSTOMER ACCOUNT"** — the vendor's name on the operator's
  own front page. It should be the operator's service name.
- **GAP — nothing else is on the page.** No logo, no support contact, no bot link, no language or
  theme switcher, and **no Terms or Privacy links** — which the admin's own Content page says
  "payment providers and application stores require at a stable address".

### Sign-in (`/account/sign-in`)

- **BUG/BLOCKER — the Telegram login widget renders Telegram's raw error, "Bot domain invalid", as a
  white unstyled box** in the middle of the dark page, where the login button should be. It looks
  like a broken third-party embed, because it is one. The page must detect the failure and show its
  own message ("Telegram sign-in is not configured for this domain — set the bot domain with
  @BotFather"), never Telegram's.
- **BLOCKER — with the widget broken there is no way at all to sign in.** The accessibility tree of
  this page contains **zero interactive elements**: no button, no link, only the failed iframe and a
  paragraph of text.
- **BLOCKER — the fallback instruction has no link.** "No Telegram button? Send /login to the bot
  and it will message you a one-time sign-in link." The bot username is already in the response that
  drives this page (`"telegramBot":"kwld_test_bot"`), so this should be a button to
  `https://t.me/<bot>?start=login`, not a sentence telling the customer to go find the bot.
- **BUG (confirmed in code) — every magic link the bot sends leads to a 404.**
  `internal/customerauthpg/signin.go:174` builds the delivered URL as
  `publicURL + "/account/sign-in/link?token=…"`, but no such page exists —
  `apps/web/app/(customer)/account/` contains only `sign-in/page.tsx`, and the built output has no
  `sign-in/link` route. The handler that redeems the token is registered at
  `GET /v1/account/auth/link` (`internal/httpapi/account.go:99`), so the issuer is one route off.
  The failure happens *after* delivery, so the customer burns a one-time credential to reach a bare
  Next.js 404 with no explanation and no way back. With the Telegram widget also broken, this leaves
  the installation with no working sign-in route at all.
- **POLISH — the service name renders as "VPN"** (the fallback when Brand → Service name is empty).
  Either require it at setup or fall back to something less placeholder-looking.
- **PAIN — signing in takes three screens.** `/` → "Open my account" → `/account` → "Sign in to
  continue" → "Sign in" → `/account/sign-in`. The middle screen adds nothing.
- **GAP — no support escape hatch and no language switcher** anywhere in the unauthenticated area.

### Public information pages (`/pages`)

- **POLISH — a bare heading and one sentence on an empty black page.** No header, no service name,
  no navigation, no way back. A customer who follows a Terms link from a payment provider lands
  somewhere with no exit.

---

## Shell and layout (all authenticated pages)

- **BLOCKER-ish — there is no desktop layout.** At 1454px the app renders a ~410px centred column
  with a floating pill tab bar at the bottom of the window; roughly 70% of the screen is empty
  black. This is a Telegram Mini App design served as the web panel. Customers who open a link on a
  laptop — which is exactly who uses the *web* panel rather than the bot — get a phone screen.
- **PAIN — the floating bottom tab bar overlaps page content** on almost every long page (store,
  support thread, checkout, profile). Content needs bottom padding equal to the bar.
- **GAP — the header never says who you are.** "VPN / your account" and nothing else: no name, no
  @handle, no email, no avatar, no customer id. A customer writing to support cannot tell the
  operator who they are, and cannot tell which account they are signed into.
- **PAIN — only five destinations in the tab bar** (Subscriptions, Store, Wallet, Support, Account).
  Orders is reachable only from a "MY ORDERS" link in the corner of the Store, and Announcements
  only from inside Support. Neither is where anyone would look.
- **GAP — no global back/breadcrumb model.** Some pages have "‹ Back", the tab roots do not, and
  Back does not always return where you came from.

---

## Overview (`/account`)

- **PAIN — the empty state is a dead end.** "No subscription yet — Once you buy one it appears here
  with its traffic, devices, and expiry" and **no button**. The one thing a customer without a
  subscription should be able to do from this screen is browse plans.

## Store (`/account/store`)

- **BUG — every plan card shows the same three buttons — Extend, Upgrade, Move down — including the
  plan the customer is currently on and the top tier.** Nothing marks the current plan. "Move down"
  on the most expensive plan and "Upgrade" on the plan you already hold are both meaningless.
- **PAIN — the plan card is not clickable**; only the button is. There is no plan detail worth
  opening anyway (see below).
- **PAIN — the plan detail page adds nothing.** `store/{planVersionId}` re-renders the identical
  card with a "Continue" button. No description, no what-you-get copy, no add-ons, no choice of
  period, no promo entry — a pure extra tap.
- **GAP — no annual/quarterly pricing, no comparison view, no "most popular" marker, no savings
  callout.** Everything is monthly and undifferentiated.
- **POLISH — "₽199.00"**: trailing `.00` on round prices is noise.
- **BUG/GAP — the "Digital goods" card promotes a shop that immediately says it does not exist.**
  Tapping "Telegram Premium and Stars, delivered to a username you choose" lands on "Digital goods
  are not sold here". Hide the card when nothing is on sale.

## Checkout (`/account/checkout`)

- **BUG — right after pressing Continue, the checkout page rendered "No purchase in progress — Pick
  a plan from the store to start one", although the purchase had just been created.** Going back to
  the store showed a "You have a purchase in progress · Starter is waiting at the confirmation step"
  banner, and Continue from there rendered the checkout correctly. The checkout page reads its state
  before the create has landed and then reports the most discouraging possible message.
- **BLOCKER — "HOW YOU PAY" offers exactly one method: "Bank transfer, approved by an operator"** —
  because no payment provider can be configured in the admin at all (the Payment providers table
  there has no "add" control). Every real payment route is missing.
- **PAIN — with one payment option, it is not preselected**, and "Confirm and pay" sits disabled
  under a yellow "Choose how you will pay before confirming". Preselect a sole option.
- **PAIN — the wallet toggle is on with a ₽0.00 balance.** "Spend my wallet balance first · ₽0.00
  available" should be disabled or hidden until there is a balance.
- **POLISH — a small "Buy" chip sits in the corner of the plan summary card** with no clear meaning
  next to the real "Confirm and pay".
- **GOOD — "This price is held for 57 more minutes", the promo field, and the promo error ("That
  code is not recognised. Check the spelling, or continue without it.") are all well done.**

## Order (`/account/orders/{id}`)

- **BLOCKER — the customer is never told how to pay.** The only method is a manual bank transfer,
  and no screen in the flow shows bank details, a card number, an amount reference, or any
  instruction. After pressing "Pay now" the page says "Waiting for an operator — Your transfer is
  checked by a person… You do not need to do anything else" — about a transfer the customer was
  never given the means to make.
- **BUG — contradictory copy on one screen.** The header says "The payment has been prepared and is
  waiting for you to complete it" while the card below says "You do not need to do anything else".
- **POLISH — "Step 0 of 3"** with the labels Paid / Setting up / Ready. Zero-indexed progress reads
  as broken; say "Waiting for payment" or "Step 1 of 3".
- **PAIN — "Try the payment again"** appears immediately after a payment was successfully prepared.
- **PAIN — "Check for an update — This asks the payment provider directly"** on an order whose
  "provider" is a human operator. The copy does not fit the manual route.
- **PAIN — "Open the subscription" on an unpaid order** leads to a subscription that is not usable.

## Orders list (`/account/orders`)

- **BUG — "Show more" does nothing** when there is no further page; it should not render.
- **GAP — no filters, no search, no receipts/invoices, no refund request.**

## Subscription (`/account/subscriptions/{id}`)

- **BLOCKER — the access link is never shown.** The "ACCESS LINK" section contains only a red
  warning and a destructive "Issue a new link". The subscription URL itself — the single most
  important thing on the page — is absent, with no copy button and no QR code.
- **PAIN — the destructive warning is displayed permanently** instead of inside a confirmation on
  the rotate action.
- **BLOCKER — no status, no expiry, no plan, no device count.** The page says "Subscription 1 · 0 MB
  used · unlimited" and nothing else. Is it active? When does it end? Which plan is it? How many
  devices are left? None of it is here.
- **BUG — an unpaid order produces a subscription that looks real.** The order is still awaiting a
  bank transfer, yet the subscription page presents an apparently usable subscription and offers to
  rotate its (non-existent) link. Only the Connect page correctly says "Still being set up".
- **GAP — no cancel, no pause, no auto-renew toggle, no "change plan" from the subscription itself.**

## Devices (`/account/devices`)

- **BUG — the page fails outright: "Something went wrong — The page could not be loaded. Try again
  in a moment."** `GET /v1/account/subscriptions/{id}/devices` answers **409** because the
  subscription is not provisioned yet, and the page renders a generic crash instead of the ordinary
  state ("your subscription is still being set up" or "no devices yet"). It is linked directly from
  the Account page, so this is the first thing a new customer hits. The RSC navigations for the same
  route also returned 503.

## Wallet (`/account/wallet`)

- **GOOD — presets, a clear amount field, honest limit copy ("From ₽100.00 to ₽50,000.00 at a time.
  You may still add ₽100,000.00 in this period"), and a sensible history empty state.**
- **POLISH — "AVAILABLE ₽0.00" and "Balance ₽0.00" are the same number twice** in one card.
- **PAIN — the sole payment method is again not preselected**, so "Add to wallet" is disabled with
  no explanation at all (checkout at least explains why).
- **GAP — no way to spend or withdraw a balance from this page, and no pending-top-up state.**

## Support (`/account/support`)

- **GOOD — the best-judged screen in the app.** "Answered here and in Telegram — whichever you open
  first", an unread badge, message count, and **relative time ("39 minutes ago") — the only place in
  either panel that uses it.**
- **GOOD — the thread's "NEW SINCE YOU OPENED THIS" divider.**
- **PAIN — file attachment is a raw unstyled `<input type=file>`** ("Choose File / No file chosen")
  in an otherwise fully designed app.
- **CROSS-PANEL GAP — customers can attach files, but the operator's Support desk has no attachment
  UI at all.** Whatever a customer sends, the operator cannot see or answer with.
- **POLISH — the list uses relative time and the thread uses absolute time** for the same messages.

## Announcements (`/account/news`)

- **POLISH — "Announcements are shown in Russian, because that is the language they are published in
  for you"** while the interface is in English. Honest, but it exposes the language split below.

## Digital goods (`/account/shop`)

- **BUG/GAP — reachable and promoted from the Store, but says "Digital goods are not sold here."**
  Two screens in the same app disagree about whether the shop exists.

## Referrals (`/account/referrals`)

- **GOOD — clear, non-apologetic empty states for both the invite programme and loyalty tiers.**
- **GAP — nothing tells the customer where to look when a programme does start**, and there is no
  link to the terms of one.

## Profile / Account (`/account/profile`)

- **BLOCKER-ish — no identity on the account page.** "Signed in with: Sign-in link" and nothing
  else. No name, no @handle, no email, no customer id to quote to support.
- **BUG — there is no plain "Sign out".** The only way out is "Sign out everywhere", two levels deep
  under Sessions and security. On a shared computer that is the wrong default and hard to find.
- **BUG — two language settings that contradict each other.** Profile → Language is **English**,
  described as "Applies here and to the messages the bot sends"; Preferences → "Language of
  messages" is **Russian**, described as "It does not change the language of this page". Two
  screens, two settings, opposite claims about the same thing.
- **PAIN — both language pickers are raw native `<select>`s**, against the repo's own UI rule.
- **PAIN — theme offers only Dark/Light**, while the operator panel also offers "System".
- **GAP — no timezone setting**, although Preferences → Quiet hours says "Times are in your own
  timezone".

## Sessions and security (`/account/security`)

- **GOOD — active sessions, "Sign out everywhere", ways to sign in with the honest "The last
  remaining method cannot be removed — it is the only way back into this account", and an account
  history.**
- **PAIN — sessions are labelled by sign-in method ("Sign-in link") with a bare IP**, not by device
  or browser. The operator panel has the mirror-image problem (raw User-Agent, no label).
- **GAP — no per-session sign-out for the current device**, no location, no "this is new" flagging.

## Notifications (`/account/preferences`)

- **GOOD — four clearly-worded switches, a quiet-hours window, and a browser-notification section
  that explains it is per-browser rather than per-account.** Well done.
- **GAP — quiet hours reference "your own timezone" that the customer cannot set.**

## Personal data (`/account/privacy`)

- **GOOD — the best page in the entire product.** Account status, contact addresses with per-purpose
  consent, and a data export that lists both **what is in it** (profile, sign-in methods, addresses,
  subscriptions, plans, orders, payments, wallet, support, invites, loyalty, consents, history) and
  **what is deliberately left out**, with the reason for each omission. Account deletion is framed
  as a request with a stated delay rather than an instant destructive button.
- This page's standard of copy and disclosure is what the rest of the app should be measured
  against.

---

## Cross-cutting

- **PAIN — timezone and date formats disagree with the operator panel.** The same order reads
  "Aug 14, 11:11 PM" here and "8/15/2026, 1:11 AM" in the admin. An operator and a customer looking
  at one order see two different days.
- **PAIN — relative time exists only in the support list**; everywhere else is absolute.
- **GAP — no offline/error retry affordance.** The one error state seen ("Something went wrong… Try
  again in a moment") has no retry button.
- **GOOD — mobile layout is correct throughout**, which is unsurprising given it is the only layout.

---

## Patterns worth spreading

1. **`/account/privacy`** — the export inventory with an explicit "what's deliberately left out".
   The model for every disclosure in the product.
2. **`/account/preferences`** — plain-language switches with a one-line consequence under each.
3. **Support list** — relative time, unread count, and "answered here and in Telegram — whichever
   you open first" as the way to describe a two-channel inbox.
4. **Checkout's promo error** — names the problem and offers the way forward in one sentence.
5. **Wallet limits copy** — states both the per-transaction range and the remaining period
   allowance, so the customer never guesses.
