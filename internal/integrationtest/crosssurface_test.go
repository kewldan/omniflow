//go:build integration

package integrationtest

// Cross-surface contract tests.
//
// Omniflow sells one product through two front doors. The Telegram bot drives
// internal/botapp; the customer web panel drives internal/accountcheckout,
// internal/accountshop, internal/accountsupport, and internal/accountreferral.
// Behind both there is one database and one set of rules, and the v0.10 gate is
// the claim that a customer who switches doors mid-journey does not switch
// products: the same action produces the same records and the same answers
// whichever surface they took it from.
//
// What these tests deliberately do not assert is that the two surfaces call the
// same function. Checkout and order orchestration genuinely is one
// implementation now — internal/botapp delegates to internal/accountcheckout —
// and a test that only proved the delegation exists would pass unchanged if the
// bot's wrapper passed the wrong locale, the wrong currency, or a default the
// browser never sends. So every pair here is exercised through each surface's
// own entry point and compared on its observable outcome: the amount, the
// state, the count, the reason, the row. Everywhere else — support, referrals,
// news — the two surfaces really are separate SQL over shared tables, and the
// comparison is the only thing keeping them honest.

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/accountsupport"
	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// crossSurfaceOptions widens the concurrent-subscription allowance.
//
// The shared testOptions caps a customer at two subscriptions, which is the
// right bound for the policy test that owns it but would make a three-order
// history impossible here: every unnamed purchase opens a new subscription, so
// the third order would be refused by a rule these tests are not about.
func crossSurfaceOptions() commercepg.Options {
	options := testOptions()
	options.Subscriptions = commerce.SubscriptionPolicy{MultiEnabled: true, MaxPerCustomer: 5}
	return options
}

// crossSurfaceBot wires the Telegram surface against the migrated database.
//
// It is built from the connection URL rather than the harness pool because that
// is the only constructor botapp exposes, and using it means these tests reach
// the bot through exactly the wiring cmd/bot does. The payment service is nil
// for the same reason it is nil on the web side: what an order costs and which
// order it becomes are settled before any provider is asked for money.
func crossSurfaceBot(
	ctx context.Context, t *testing.T, harness *harness, orders *commercepg.Store,
) (*botapp.PostgresStore, *botapp.Commerce) {
	t.Helper()
	store, err := botapp.NewPostgresStore(ctx, harness.url)
	if err != nil {
		t.Fatalf("build bot store: %v", err)
	}
	t.Cleanup(store.Close)
	surface := botapp.NewCommerce(slog.New(slog.DiscardHandler), store, orders, nil,
		botapp.CommerceSettings{
			Currency: "RUB", PublicURL: "https://example.test",
			TermsURL: "https://example.test/terms", MultiSubscription: true,
		})
	return store, surface
}

// crossSurfaceLinkTelegram gives a customer the Telegram identity that makes
// them reachable in the chat.
//
// A customer who uses both surfaces has one by definition, and the bot's
// delivery queries are keyed on it. Without the link there is no chat for a
// reply to arrive in, so the counter the bot maintains would never move and a
// comparison against it would be comparing a real number with the absence of a
// customer.
// The subject is stored as text because an identity provider's subject is
// whatever that provider says it is; the bot casts it back to a number itself.
func crossSurfaceLinkTelegram(
	ctx context.Context, t *testing.T, harness *harness, customerID, telegramID string,
) {
	t.Helper()
	if _, err := harness.pool.Exec(ctx, `INSERT INTO identities
		(user_id, provider, provider_subject, verified_at, status)
		VALUES ($1::uuid, 'telegram', $2, now(), 'active')`, customerID, telegramID); err != nil {
		t.Fatalf("link the Telegram identity: %v", err)
	}
}

// crossSurfaceOpenCheckouts counts the checkouts a customer has in flight. One
// is the invariant; the number is reported so a failure says how it broke.
func crossSurfaceOpenCheckouts(ctx context.Context, t *testing.T, harness *harness, customerID string) int {
	t.Helper()
	var open int
	if err := harness.pool.QueryRow(ctx, `SELECT count(*)::integer FROM bot_checkout_sessions
		WHERE user_id = $1::uuid AND order_id IS NULL`, customerID).Scan(&open); err != nil {
		t.Fatalf("count open checkouts: %v", err)
	}
	return open
}

// crossSurfaceOrderCount is how many orders a customer actually ended up with,
// which is the outcome a duplicate-charge bug shows up in first.
func crossSurfaceOrderCount(ctx context.Context, t *testing.T, harness *harness, customerID string) int {
	t.Helper()
	var orders int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*)::integer FROM orders WHERE user_id = $1::uuid`, customerID).Scan(&orders); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	return orders
}

// crossSurfaceQuoteFacts is a quote reduced to the numbers a customer is shown
// before they commit.
//
// Comparing this rather than the quote struct is the point: two surfaces are
// allowed to project a quote into different shapes, and they are not allowed to
// disagree about what the purchase costs. Every field here is one line of the
// price breakdown on screen.
type crossSurfaceQuoteFacts struct {
	Currency      string
	Subtotal      int64
	AddonMinor    int64
	DiscountMinor int64
	WalletBalance int64
	WalletApplied int64
	ExternalMinor int64
	PromoCode     string
	Rejection     string
}

func crossSurfaceQuoteOf(quote commerce.CheckoutQuote) crossSurfaceQuoteFacts {
	return crossSurfaceQuoteFacts{
		Currency: quote.Subtotal.Currency, Subtotal: quote.Subtotal.Amount,
		AddonMinor: quote.AddonMinor, DiscountMinor: quote.DiscountMinor,
		WalletBalance: quote.WalletBalanceMinor, WalletApplied: quote.WalletAppliedMinor,
		ExternalMinor: quote.ExternalMinor, PromoCode: quote.PromoCode,
		Rejection: quote.PromoRejection,
	}
}

// crossSurfaceOrderFacts is an order reduced to what the customer and the
// accountant both have to agree on: what it was for, what it cost, where it got
// to, and what the screen says is happening to it now.
type crossSurfaceOrderFacts struct {
	State         commerce.OrderState
	Phase         commerce.PaymentPhase
	Operation     string
	Currency      string
	PlanName      string
	SubtotalMinor int64
	DiscountMinor int64
	WalletMinor   int64
	ExternalMinor int64
	PaidMinor     int64
}

func crossSurfaceOrderOf(order accountcheckout.OrderSummary) crossSurfaceOrderFacts {
	return crossSurfaceOrderFacts{
		State: order.State, Phase: order.Phase, Operation: order.Operation,
		Currency: order.Currency, PlanName: order.PlanName,
		SubtotalMinor: order.SubtotalMinor, DiscountMinor: order.DiscountMinor,
		WalletMinor: order.WalletMinor, ExternalMinor: order.ExternalMinor,
		PaidMinor: order.PaidMinor,
	}
}

// TestCrossSurfaceCheckoutProducesTheSameQuoteAndOrder is the money pair.
//
// Two customers in identical positions — same plan version, same wallet
// balance, same promo code, same add-on, same wallet setting — configure and
// confirm a purchase, one entirely through the bot's methods and one entirely
// through the web panel's. If the price breakdown or the resulting order
// differed by a single minor unit, one of the two would have been charged
// something nobody could explain to the other.
//
// The two customers are separate on purpose. A shared checkout means one person
// cannot hold a bot checkout and a web checkout at once, so the only way to
// price the same purchase twice is to price it for two people who are in the
// same position.
func TestCrossSurfaceCheckoutProducesTheSameQuoteAndOrder(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	orders := commercepg.New(harness.pool, nil, crossSurfaceOptions())
	web := newAccountCheckout(t, harness, orders)
	botStore, botCommerce := crossSurfaceBot(ctx, t, harness, orders)

	planVersionID := harness.catalog(ctx, t, "cross-quote-plan", 50000)
	addonVersionID := harness.addon(ctx, t, planVersionID, "cross-quote-traffic", 10000, 10737418240, "remaining_period")
	harness.promotion(ctx, t, "crossten", harness.planIDOf(ctx, t, planVersionID), 5)

	botCustomer := harness.customer(ctx, t)
	webCustomer := harness.customer(ctx, t)
	harness.seedWallet(ctx, t, botCustomer, "cross-quote-bot", 20000)
	harness.seedWallet(ctx, t, webCustomer, "cross-quote-web", 20000)

	// The bot's own sequence: open, attach the add-on, fund it from the wallet,
	// enter the code, read the price.
	botSession, err := botStore.OpenCheckout(ctx, botCustomer, planVersionID, "purchase", "RUB", "", nil)
	if err != nil {
		t.Fatalf("open the bot checkout: %v", err)
	}
	if err = botStore.ToggleCheckoutAddon(ctx, botSession.ID, addonVersionID); err != nil {
		t.Fatalf("attach the add-on in the bot: %v", err)
	}
	if botSession, err = botStore.SetCheckoutWallet(ctx, botSession.ID, true); err != nil {
		t.Fatalf("apply the wallet in the bot: %v", err)
	}
	if botSession, err = botCommerce.ApplyPromo(ctx, botSession, "crossten"); err != nil {
		t.Fatalf("apply the promo in the bot: %v", err)
	}
	botQuote, err := botCommerce.Quote(ctx, botSession)
	if err != nil {
		t.Fatalf("price the bot checkout: %v", err)
	}

	// The panel's own sequence, which is the same journey expressed as HTTP
	// requests rather than taps.
	if _, err = web.Open(ctx, webCustomer, "en", accountcheckout.OpenRequest{
		PlanVersionID: planVersionID, Operation: "purchase",
	}); err != nil {
		t.Fatalf("open the web checkout: %v", err)
	}
	if _, err = web.ToggleAddon(ctx, webCustomer, "en", addonVersionID); err != nil {
		t.Fatalf("attach the add-on on the web: %v", err)
	}
	applyWallet := true
	if _, err = web.Update(ctx, webCustomer, "en", accountcheckout.UpdateRequest{
		ApplyWallet: &applyWallet,
	}); err != nil {
		t.Fatalf("apply the wallet on the web: %v", err)
	}
	webView, err := web.ApplyPromoCode(ctx, webCustomer, "en", "crossten")
	if err != nil {
		t.Fatalf("apply the promo on the web: %v", err)
	}

	botFacts, webFacts := crossSurfaceQuoteOf(botQuote), crossSurfaceQuoteOf(webView.Quote)
	if botFacts != webFacts {
		t.Fatalf("the two surfaces priced the same purchase differently:\n  bot %+v\n  web %+v",
			botFacts, webFacts)
	}
	// The comparison would also pass if both surfaces had quietly dropped the
	// add-on, the discount, and the wallet, so each part of the breakdown is
	// pinned to a number as well as to its counterpart.
	if botFacts.Subtotal != 50000 || botFacts.AddonMinor != 10000 {
		t.Fatalf("the plan and add-on did not reach the quote: %+v", botFacts)
	}
	// The campaign discounts the plan and not the add-on, which is a rule both
	// surfaces inherit from the pricing code rather than restate. Ten percent of
	// the 50000 plan is 5000, leaving 55000 for the wallet to bite into.
	if botFacts.DiscountMinor != 5000 {
		t.Fatalf("ten percent of the 50000 plan is 5000, the quote says %d", botFacts.DiscountMinor)
	}
	if botFacts.WalletApplied != 20000 || botFacts.ExternalMinor != 35000 {
		t.Fatalf("the wallet split reads %+v", botFacts)
	}
	if botFacts.PromoCode != "CROSSTEN" || botFacts.Rejection != "" {
		t.Fatalf("the accepted code did not survive into the quote: %+v", botFacts)
	}

	// Confirming either one produces one order, and the two orders agree.
	botOrderID, err := botCommerce.Confirm(ctx, botSession, botapp.Plan{}, botapp.Customer{})
	if err != nil {
		t.Fatalf("confirm in the bot: %v", err)
	}
	botOrder, err := botStore.Order(ctx, botCustomer, botOrderID, "en")
	if err != nil {
		t.Fatalf("read the bot order: %v", err)
	}
	webOrder, err := web.ConfirmCheckout(ctx, webCustomer, "en")
	if err != nil {
		t.Fatalf("confirm on the web: %v", err)
	}

	if crossSurfaceOrderOf(botOrder) != crossSurfaceOrderOf(webOrder) {
		t.Fatalf("the two surfaces recorded different orders:\n  bot %+v\n  web %+v",
			crossSurfaceOrderOf(botOrder), crossSurfaceOrderOf(webOrder))
	}
	// The order has to carry the figures the customer was shown rather than a
	// second computation of them. A quote that agreed across surfaces but did not
	// survive into the order would still charge somebody a number they never saw.
	if botOrder.DiscountMinor != botFacts.DiscountMinor ||
		botOrder.WalletMinor != botFacts.WalletApplied ||
		botOrder.ExternalMinor != botFacts.ExternalMinor {
		t.Fatalf("the order does not carry the quoted amounts:\n  quote %+v\n  order %+v",
			botFacts, crossSurfaceOrderOf(botOrder))
	}
	// Neither wallet has moved yet, and both have moved the same amount, which is
	// nothing. The wallet contribution is recorded on the order and debited when
	// the payment settles, so an order still waiting for money must leave the
	// balance where it was — on either surface. A wallet spent at confirmation on
	// one of them would be money taken for a purchase that may never complete.
	for _, subject := range []struct {
		surface, customer string
	}{{"bot", botCustomer}, {"web", webCustomer}} {
		if count := crossSurfaceOrderCount(ctx, t, harness, subject.customer); count != 1 {
			t.Fatalf("the %s customer ended up with %d orders", subject.surface, count)
		}
		if balance := harness.walletBalance(ctx, t, subject.customer); balance != 20000 {
			t.Fatalf("the %s customer's wallet reads %d before the order was paid",
				subject.surface, balance)
		}
	}
}

// TestCrossSurfaceCheckoutSessionIsShared is the rule that makes the two
// surfaces one purchase rather than two.
//
// A customer holds at most one open checkout. Starting one in the browser
// supersedes the one they left open in the chat, because there is only ever one
// row to open — and a customer who abandoned a half-configured purchase in
// Telegram, then bought on the web, must not discover two orders afterwards.
func TestCrossSurfaceCheckoutSessionIsShared(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	orders := commercepg.New(harness.pool, nil, crossSurfaceOptions())
	web := newAccountCheckout(t, harness, orders)
	botStore, botCommerce := crossSurfaceBot(ctx, t, harness, orders)

	planVersionID := harness.catalog(ctx, t, "cross-session-plan", 34900)
	customerID := harness.customer(ctx, t)

	fromBot, err := botStore.OpenCheckout(ctx, customerID, planVersionID, "purchase", "RUB", "", nil)
	if err != nil {
		t.Fatalf("open in the bot: %v", err)
	}
	fromWeb, err := web.Open(ctx, customerID, "en", accountcheckout.OpenRequest{
		PlanVersionID: planVersionID, Operation: "purchase",
	})
	if err != nil {
		t.Fatalf("open on the web: %v", err)
	}
	if fromWeb.ID == fromBot.ID {
		t.Fatal("the web resumed the bot's session row rather than replacing it")
	}
	if open := crossSurfaceOpenCheckouts(ctx, t, harness, customerID); open != 1 {
		t.Fatalf("the customer holds %d open checkouts, want exactly one", open)
	}
	// The bot now reads the checkout the browser started. Neither surface keeps a
	// private copy, so there is nothing to reconcile between them.
	resumed, found, err := botStore.Checkout(ctx, customerID)
	if err != nil || !found {
		t.Fatalf("the bot cannot see the web's checkout: %v (found=%v)", err, found)
	}
	if resumed.ID != fromWeb.ID {
		t.Fatalf("the bot resumed %q while the web holds %q", resumed.ID, fromWeb.ID)
	}

	// And the other direction: opening again in the chat supersedes the browser's.
	reopened, err := botStore.OpenCheckout(ctx, customerID, planVersionID, "purchase", "RUB", "", nil)
	if err != nil {
		t.Fatalf("reopen in the bot: %v", err)
	}
	view, err := web.View(ctx, customerID, "en")
	if err != nil {
		t.Fatalf("the web cannot see the bot's checkout: %v", err)
	}
	if view.ID != reopened.ID {
		t.Fatalf("the web is showing %q while the bot holds %q", view.ID, reopened.ID)
	}
	if open := crossSurfaceOpenCheckouts(ctx, t, harness, customerID); open != 1 {
		t.Fatalf("superseding left %d open checkouts", open)
	}

	// Confirming from either surface closes the one checkout for both, so the
	// abandoned session cannot be confirmed a second time from the other door.
	if _, err = botCommerce.Confirm(ctx, reopened, botapp.Plan{}, botapp.Customer{}); err != nil {
		t.Fatalf("confirm in the bot: %v", err)
	}
	if _, err = web.View(ctx, customerID, "en"); !errors.Is(err, accountcheckout.ErrNoCheckout) {
		t.Fatalf("the web still holds a checkout after the bot confirmed it: %v", err)
	}
	if count := crossSurfaceOrderCount(ctx, t, harness, customerID); count != 1 {
		t.Fatalf("two surfaces and one purchase produced %d orders", count)
	}
}

// TestCrossSurfacePromoRefusalsCarryTheSameReason covers the four ways a code is
// turned away.
//
// The reason is a stable machine value both panels look up their own wording
// for, so a surface that reported a different one would show a customer with an
// exhausted code a message telling them to check for a typo. The bot reads it
// off the stored session and the panel reads it off the priced view — two
// different projections that have to arrive at the same word.
func TestCrossSurfacePromoRefusalsCarryTheSameReason(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	orders := commercepg.New(harness.pool, nil, crossSurfaceOptions())
	web := newAccountCheckout(t, harness, orders)
	botStore, botCommerce := crossSurfaceBot(ctx, t, harness, orders)

	planVersionID := harness.catalog(ctx, t, "cross-promo-plan", 34900)
	planID := harness.planIDOf(ctx, t, planVersionID)
	// A campaign scoped to a different plan is what an ineligible code looks
	// like; one with no plan links at all would apply to everything.
	otherPlanID := harness.planIDOf(ctx, t, harness.catalog(ctx, t, "cross-promo-other", 9900))
	harness.promotion(ctx, t, "crosselse", otherPlanID, 5)
	harness.promotion(ctx, t, "crossonce", planID, 1)

	botCustomer := harness.customer(ctx, t)
	webCustomer := harness.customer(ctx, t)

	botSession, err := botStore.OpenCheckout(ctx, botCustomer, planVersionID, "purchase", "RUB", "", nil)
	if err != nil {
		t.Fatalf("open in the bot: %v", err)
	}
	if _, err = web.Open(ctx, webCustomer, "en", accountcheckout.OpenRequest{
		PlanVersionID: planVersionID, Operation: "purchase",
	}); err != nil {
		t.Fatalf("open on the web: %v", err)
	}

	// The accepted code is applied last so the state it leaves behind is the one
	// the confirmation below acts on.
	for _, attempt := range []struct{ code, want string }{
		{"!!", accountcheckout.PromoInvalid},
		{"NOSUCHCODE", accountcheckout.PromoUnknown},
		{"CROSSELSE", accountcheckout.PromoIneligible},
		{"  crossonce  ", ""},
	} {
		if botSession, err = botCommerce.ApplyPromo(ctx, botSession, attempt.code); err != nil {
			t.Fatalf("apply %q in the bot: %v", attempt.code, err)
		}
		webView, webErr := web.ApplyPromoCode(ctx, webCustomer, "en", attempt.code)
		if webErr != nil {
			t.Fatalf("apply %q on the web: %v", attempt.code, webErr)
		}
		if botSession.PromoRejection != webView.PromoRejection {
			t.Fatalf("applying %q was refused as %q in the bot and %q on the web",
				attempt.code, botSession.PromoRejection, webView.PromoRejection)
		}
		if botSession.PromoRejection != attempt.want {
			t.Fatalf("applying %q reported %q, want %q", attempt.code, botSession.PromoRejection, attempt.want)
		}
	}

	// One redemption per customer, so spending it makes the next attempt
	// exhausted on both surfaces — not unknown, and not ineligible.
	if _, err = botCommerce.Confirm(ctx, botSession, botapp.Plan{}, botapp.Customer{}); err != nil {
		t.Fatalf("confirm in the bot: %v", err)
	}
	if _, err = web.ConfirmCheckout(ctx, webCustomer, "en"); err != nil {
		t.Fatalf("confirm on the web: %v", err)
	}
	if botSession, err = botStore.OpenCheckout(
		ctx, botCustomer, planVersionID, "purchase", "RUB", "", nil,
	); err != nil {
		t.Fatalf("reopen in the bot: %v", err)
	}
	if _, err = web.Open(ctx, webCustomer, "en", accountcheckout.OpenRequest{
		PlanVersionID: planVersionID, Operation: "purchase",
	}); err != nil {
		t.Fatalf("reopen on the web: %v", err)
	}
	if botSession, err = botCommerce.ApplyPromo(ctx, botSession, "CROSSONCE"); err != nil {
		t.Fatalf("reapply in the bot: %v", err)
	}
	webAgain, err := web.ApplyPromoCode(ctx, webCustomer, "en", "CROSSONCE")
	if err != nil {
		t.Fatalf("reapply on the web: %v", err)
	}
	if botSession.PromoRejection != webAgain.PromoRejection {
		t.Fatalf("a spent code reads %q in the bot and %q on the web",
			botSession.PromoRejection, webAgain.PromoRejection)
	}
	if botSession.PromoRejection != accountcheckout.PromoExhausted {
		t.Fatalf("a spent code reported %q", botSession.PromoRejection)
	}
}

// TestCrossSurfaceOrderHistoryAgrees compares the two history screens.
//
// The customer's orders are made from both doors and left in three different
// states — awaiting payment, settled, cancelled — because a history that agreed
// only about a pending order would prove nothing about the one the customer
// actually wants to look up. The comparison covers the payment phase as well as
// the state: the phase is what the screen renders, and it is folded from three
// records rather than read from one.
//
// The locale is deliberately Russian. The bot passes its own Locale type and
// the panel passes a string, and a wrapper that dropped the value would leave
// one surface showing the English plan name to a Russian customer.
func TestCrossSurfaceOrderHistoryAgrees(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	orders := commercepg.New(harness.pool, nil, crossSurfaceOptions())
	web := newAccountCheckout(t, harness, orders)
	botStore, botCommerce := crossSurfaceBot(ctx, t, harness, orders)

	planVersionID := harness.catalog(ctx, t, "cross-history-plan", 25000)
	if _, err := harness.pool.Exec(ctx,
		`UPDATE plan_localizations SET name = 'Тариф на месяц'
		 WHERE plan_id = $1::uuid AND locale = 'ru'`,
		harness.planIDOf(ctx, t, planVersionID)); err != nil {
		t.Fatalf("localize the plan into Russian: %v", err)
	}
	customerID := harness.customer(ctx, t)

	// Two orders opened and confirmed in the chat, one in the browser. Nothing is
	// settled yet, so every checkout resolves its subscription target the same
	// way and the three orders differ only in where they came from.
	confirmInBot := func(label string) string {
		t.Helper()
		session, err := botStore.OpenCheckout(ctx, customerID, planVersionID, "purchase", "RUB", "", nil)
		if err != nil {
			t.Fatalf("open %s in the bot: %v", label, err)
		}
		orderID, err := botCommerce.Confirm(ctx, session, botapp.Plan{}, botapp.Customer{})
		if err != nil {
			t.Fatalf("confirm %s in the bot: %v", label, err)
		}
		return orderID
	}
	pendingID := confirmInBot("the pending order")
	if _, err := web.Open(ctx, customerID, "en", accountcheckout.OpenRequest{
		PlanVersionID: planVersionID, Operation: "purchase",
	}); err != nil {
		t.Fatalf("open the settled order on the web: %v", err)
	}
	settled, err := web.ConfirmCheckout(ctx, customerID, "en")
	if err != nil {
		t.Fatalf("confirm the settled order on the web: %v", err)
	}
	cancelledID := confirmInBot("the cancelled order")

	// One is paid through the provider path, and one is cancelled from the panel
	// after having been created in the chat — a cross-surface mutation whose
	// result both histories have to show.
	if classification := harness.settle(ctx, t, orders, settled.ID, 25000, "cross-history-event"); classification != "paid" {
		t.Fatalf("the settlement was classified %q", classification)
	}
	if err = web.CancelOrder(ctx, customerID, cancelledID); err != nil {
		t.Fatalf("cancel from the web: %v", err)
	}

	botHistory, err := botStore.Orders(ctx, customerID, botapp.Locale("ru"), 50)
	if err != nil {
		t.Fatalf("read the bot history: %v", err)
	}
	webHistory, err := web.Orders(ctx, customerID, "ru", accountcheckout.Cursor{}, 50)
	if err != nil {
		t.Fatalf("read the web history: %v", err)
	}
	if len(botHistory) != 3 || len(webHistory) != 3 {
		t.Fatalf("the bot lists %d orders and the web lists %d, want three each",
			len(botHistory), len(webHistory))
	}
	for index := range botHistory {
		if botHistory[index].ID != webHistory[index].ID {
			t.Fatalf("position %d is order %s in the bot and %s on the web",
				index, botHistory[index].ID, webHistory[index].ID)
		}
		if crossSurfaceOrderOf(botHistory[index]) != crossSurfaceOrderOf(webHistory[index]) {
			t.Fatalf("order %s reads differently on the two surfaces:\n  bot %+v\n  web %+v",
				botHistory[index].ID,
				crossSurfaceOrderOf(botHistory[index]), crossSurfaceOrderOf(webHistory[index]))
		}
		if botHistory[index].PlanName != "Тариф на месяц" {
			t.Fatalf("order %s shows the plan as %q in Russian",
				botHistory[index].ID, botHistory[index].PlanName)
		}
	}

	// The three states are actually distinct, so the agreement above is about
	// three different answers rather than three copies of one.
	phases := make(map[string]commerce.PaymentPhase, 3)
	for _, order := range webHistory {
		phases[order.ID] = order.Phase
	}
	if phases[pendingID] != commerce.PaymentPhaseAwaitingAction {
		t.Fatalf("the unpaid order is in phase %q", phases[pendingID])
	}
	if phases[settled.ID] != commerce.PaymentPhaseProvisioning {
		t.Fatalf("the settled order is in phase %q", phases[settled.ID])
	}
	if phases[cancelledID] != commerce.PaymentPhaseCancelled {
		t.Fatalf("the cancelled order is in phase %q", phases[cancelledID])
	}

	// A single order read by identifier answers the same on both surfaces too,
	// which is the screen a customer reaches from a link rather than a list.
	for _, orderID := range []string{pendingID, settled.ID, cancelledID} {
		fromBot, botErr := botStore.Order(ctx, customerID, orderID, botapp.Locale("ru"))
		if botErr != nil {
			t.Fatalf("read %s in the bot: %v", orderID, botErr)
		}
		fromWeb, _, webErr := web.Order(ctx, customerID, orderID, "ru")
		if webErr != nil {
			t.Fatalf("read %s on the web: %v", orderID, webErr)
		}
		if crossSurfaceOrderOf(fromBot) != crossSurfaceOrderOf(fromWeb) {
			t.Fatalf("order %s read by identifier differs:\n  bot %+v\n  web %+v",
				orderID, crossSurfaceOrderOf(fromBot), crossSurfaceOrderOf(fromWeb))
		}
	}
}

// TestCrossSurfaceSupportReadStateAgrees is the read-state pair, measured
// through the bot's own conversation reader.
//
// The web side of this is already covered in accountsupport_test.go, so what is
// proved here is the half that test does not reach: the per-message read stamp
// and the aggregate badge the chat renders. The two surfaces count unread
// differently on purpose — the panel counts messages with no read stamp, the bot
// reads the delivery counter Telegram maintains — and the contract is that a
// message read on either surface is read on both regardless of which counter is
// being consulted.
func TestCrossSurfaceSupportReadStateAgrees(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	support := newAccountSupport(t, harness)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "cross-surface-desk@example.test")
	botStore, _ := crossSurfaceBot(ctx, t, harness, commercepg.New(harness.pool, nil, crossSurfaceOptions()))

	customerID := harness.customer(ctx, t)
	crossSurfaceLinkTelegram(ctx, t, harness, customerID, "770001")
	ticketID := newCustomerTicket(ctx, t, support, customerID, "Handover")

	// One operator reply, delivered to the chat. Delivery is what raises the
	// bot's badge, so this is the state a Telegram customer actually sees.
	if _, err := operations.Reply(ctx, panelpg.ReplyInput{
		TicketID: ticketID, Body: "Try the other profile", DedupeKey: "cross-support-1",
	}, actor); err != nil {
		t.Fatalf("operator reply: %v", err)
	}
	crossSurfaceDeliverReplies(ctx, t, botStore, 1)

	botUnread, err := botStore.UnreadSupportCount(ctx, customerID)
	if err != nil {
		t.Fatalf("count unread in the bot: %v", err)
	}
	conversation, err := support.Conversation(ctx, customerID, ticketID)
	if err != nil {
		t.Fatalf("read the conversation on the web: %v", err)
	}
	if botUnread != 1 || conversation.Ticket.Unread != 1 {
		t.Fatalf("the bot counts %d unread and the web counts %d before anybody read it",
			botUnread, conversation.Ticket.Unread)
	}

	// Read it in the browser. The chat must not still show it as new, and the
	// message itself must carry a read stamp the bot's own reader can see.
	if _, err = support.MarkRead(ctx, customerID, ticketID); err != nil {
		t.Fatalf("mark read on the web: %v", err)
	}
	if botUnread, err = botStore.UnreadSupportCount(ctx, customerID); err != nil {
		t.Fatalf("recount unread in the bot: %v", err)
	}
	if botUnread != 0 {
		t.Fatalf("the bot still counts %d unread after the web read the ticket", botUnread)
	}
	crossSurfaceAssertBotReadState(ctx, t, botStore, customerID, ticketID, "the web read it")

	// And the other way round: a second reply read in the chat must not stay bold
	// in the browser.
	if _, err = operations.Reply(ctx, panelpg.ReplyInput{
		TicketID: ticketID, Body: "Any change?", DedupeKey: "cross-support-2",
	}, actor); err != nil {
		t.Fatalf("second operator reply: %v", err)
	}
	crossSurfaceDeliverReplies(ctx, t, botStore, 1)
	if botUnread, err = botStore.UnreadSupportCount(ctx, customerID); err != nil {
		t.Fatalf("recount unread in the bot: %v", err)
	}
	if conversation, err = support.Conversation(ctx, customerID, ticketID); err != nil {
		t.Fatalf("re-read the conversation on the web: %v", err)
	}
	if botUnread != 1 || conversation.Ticket.Unread != 1 {
		t.Fatalf("after a new reply the bot counts %d and the web counts %d",
			botUnread, conversation.Ticket.Unread)
	}
	if err = botStore.MarkTicketRead(ctx, customerID, ticketID); err != nil {
		t.Fatalf("mark read in the bot: %v", err)
	}
	if conversation, err = support.Conversation(ctx, customerID, ticketID); err != nil {
		t.Fatalf("re-read the conversation on the web: %v", err)
	}
	if conversation.Ticket.Unread != 0 {
		t.Fatalf("the web reports %d unread after the bot read it", conversation.Ticket.Unread)
	}
	for _, message := range conversation.Messages {
		if message.Unread {
			t.Fatal("a message read in the chat is still unread in the browser")
		}
	}
	crossSurfaceAssertBotReadState(ctx, t, botStore, customerID, ticketID, "the bot read it")

	// The customer's own words are never unread to them, on either surface, so
	// the agreement above is about operator replies rather than about everything
	// being stamped indiscriminately.
	sawOperator, sawCustomer := false, false
	for _, message := range conversation.Messages {
		switch message.Author {
		case accountsupport.AuthorOperator:
			sawOperator = true
		case accountsupport.AuthorCustomer:
			sawCustomer = true
		}
	}
	if !sawOperator || !sawCustomer {
		t.Fatalf("the conversation is missing a side of it: %+v", conversation.Messages)
	}
}

// crossSurfaceDeliverReplies simulates the Telegram delivery worker, which is
// what raises the bot's unread badge. Without it the chat would show nothing new
// however many replies an operator wrote, because the counter is a delivery
// record rather than a message count.
// The expected count is asserted rather than inferred: a customer the queue
// cannot see would deliver nothing, and every assertion that follows would then
// be comparing two zeroes and passing for the wrong reason.
func crossSurfaceDeliverReplies(ctx context.Context, t *testing.T, store *botapp.PostgresStore, want int) {
	t.Helper()
	pending, err := store.PendingOperatorReplies(ctx, 10)
	if err != nil {
		t.Fatalf("claim pending replies: %v", err)
	}
	if len(pending) != want {
		t.Fatalf("the delivery queue holds %d replies, want %d", len(pending), want)
	}
	for _, reply := range pending {
		if err = store.MarkOperatorReplyDelivered(ctx, reply.MessageID); err != nil {
			t.Fatalf("record delivery of message %d: %v", reply.MessageID, err)
		}
	}
}

// crossSurfaceAssertBotReadState checks the bot's own conversation reader, which
// is the projection a Telegram customer actually looks at.
func crossSurfaceAssertBotReadState(
	ctx context.Context, t *testing.T, store *botapp.PostgresStore, customerID, ticketID, after string,
) {
	t.Helper()
	ticket, messages, err := store.Ticket(ctx, customerID, ticketID)
	if err != nil {
		t.Fatalf("read the ticket in the bot: %v", err)
	}
	if ticket.UnreadCount != 0 {
		t.Fatalf("the bot's ticket still counts %d unread after %s", ticket.UnreadCount, after)
	}
	for _, message := range messages {
		if message.Sender == "customer" {
			continue
		}
		if message.ReadAt.IsZero() {
			t.Fatalf("operator message %d carries no read stamp after %s", message.ID, after)
		}
	}
}

// TestCrossSurfaceReferralCountsAgreeAfterAReversal is the referral pair, and
// the reason it exists is the reversal.
//
// An operator reverses a reward when a referral turns out to be abuse. The
// reward row stays — the ledger is append-only and "granted then taken back" is
// a different fact from "never granted" — so an aggregate written before the
// reversal columns existed would keep counting money the customer no longer has,
// and would keep a cap slot occupied by a referral that was rejected. Both
// surfaces have to exclude it, and this pins that so it cannot regress.
func TestCrossSurfaceReferralCountsAgreeAfterAReversal(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	referrals := referralService(t, harness)
	botStore, _ := crossSurfaceBot(ctx, t, harness, commercepg.New(harness.pool, nil, crossSurfaceOptions()))

	// A programme with a cap, because the remaining-slots computation is the part
	// of the summary that is derived rather than counted.
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_programs
		(singleton, enabled, currency, inviter_reward_minor, invitee_reward_minor,
		 inviter_reward_cap, terms_url)
		VALUES (true, true, 'RUB', 20000, 10000, 3, 'https://example.test/referrals')`); err != nil {
		t.Fatalf("configure the referral programme: %v", err)
	}

	inviter := harness.seedReferralAccount(ctx, t, "cross")
	rewarded := []string{harness.customer(ctx, t), harness.customer(ctx, t)}
	waiting := harness.customer(ctx, t)

	for _, invitee := range rewarded {
		if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions
			(referred_user_id, referrer_user_id, code, qualified_at)
			VALUES ($1::uuid, $2::uuid, $3, now())`,
			invitee, inviter.customerID, referralCodeFor("cross")); err != nil {
			t.Fatalf("attribute %s: %v", invitee, err)
		}
	}
	// One invitation that has not qualified yet, so "invited" and "qualified" are
	// different numbers and a surface that confused them would be caught.
	if _, err := harness.pool.Exec(ctx, `INSERT INTO referral_attributions
		(referred_user_id, referrer_user_id, code)
		VALUES ($1::uuid, $2::uuid, $3)`,
		waiting, inviter.customerID, referralCodeFor("cross")); err != nil {
		t.Fatalf("attribute the pending invitation: %v", err)
	}

	rewardIDs := make([]string, 0, len(rewarded))
	for index, invitee := range rewarded {
		key := "cross-reward-" + invitee
		var transactionID string
		if err := harness.pool.QueryRow(ctx, `INSERT INTO ledger_transactions
			(type, reference_type, reference_id, idempotency_key)
			VALUES ('referral_reward', 'referral', $1, $2) RETURNING id::text`,
			invitee, key).Scan(&transactionID); err != nil {
			t.Fatalf("seed reward transaction %d: %v", index, err)
		}
		var rewardID string
		if err := harness.pool.QueryRow(ctx, `INSERT INTO referral_rewards
			(referred_user_id, beneficiary_user_id, role, order_id, amount_minor, currency,
			 ledger_transaction_id)
			VALUES ($1::uuid, $2::uuid, 'inviter', $3::uuid, 20000, 'RUB', $4::uuid)
			RETURNING id::text`,
			invitee, inviter.customerID, inviter.orderID, transactionID).Scan(&rewardID); err != nil {
			t.Fatalf("seed reward %d: %v", index, err)
		}
		rewardIDs = append(rewardIDs, rewardID)
	}

	assertAgreement := func(stage string, invited, qualified, earned int64, slots int) {
		t.Helper()
		fromBot, err := botStore.ReferralSummary(ctx, inviter.customerID)
		if err != nil {
			t.Fatalf("read the bot summary %s: %v", stage, err)
		}
		fromWeb, err := referrals.Referrals(ctx, inviter.customerID, "", 0)
		if err != nil {
			t.Fatalf("read the web summary %s: %v", stage, err)
		}
		if fromBot.Code != fromWeb.Code {
			t.Fatalf("%s: the surfaces issued different codes, %q and %q",
				stage, fromBot.Code, fromWeb.Code)
		}
		if fromBot.Invited != fromWeb.Invited || fromBot.Qualified != fromWeb.Qualified {
			t.Fatalf("%s: the bot counts %d invited and %d qualified, the web counts %d and %d",
				stage, fromBot.Invited, fromBot.Qualified, fromWeb.Invited, fromWeb.Qualified)
		}
		if fromBot.RewardedMinor != fromWeb.RewardedMinor {
			t.Fatalf("%s: the bot reports %d earned and the web reports %d",
				stage, fromBot.RewardedMinor, fromWeb.RewardedMinor)
		}
		if fromBot.RemainingSlots == nil || fromWeb.RemainingSlots == nil {
			t.Fatalf("%s: a capped programme reported no remaining slots (bot %v, web %v)",
				stage, fromBot.RemainingSlots, fromWeb.RemainingSlots)
		}
		if *fromBot.RemainingSlots != *fromWeb.RemainingSlots {
			t.Fatalf("%s: the bot offers %d remaining slots and the web offers %d",
				stage, *fromBot.RemainingSlots, *fromWeb.RemainingSlots)
		}
		// Agreeing on the wrong number would still be a defect, so the pair is
		// checked against what the seeded records actually mean.
		if fromBot.Invited != invited || fromBot.Qualified != qualified {
			t.Fatalf("%s: counts read %d invited and %d qualified, want %d and %d",
				stage, fromBot.Invited, fromBot.Qualified, invited, qualified)
		}
		if fromBot.RewardedMinor != earned {
			t.Fatalf("%s: earned reads %d, want %d", stage, fromBot.RewardedMinor, earned)
		}
		if *fromBot.RemainingSlots != slots {
			t.Fatalf("%s: remaining slots read %d, want %d", stage, *fromBot.RemainingSlots, slots)
		}
	}

	assertAgreement("with two granted rewards", 3, 2, 40000, 1)

	// The operator reverses one. The compensating transaction is a separate
	// ledger record rather than an edit, which is why the reward row survives to
	// be miscounted in the first place.
	var reversalID string
	if err := harness.pool.QueryRow(ctx, `INSERT INTO ledger_transactions
		(type, reference_type, reference_id, idempotency_key, reason)
		VALUES ('correction', 'referral', $1, $2, 'abuse review') RETURNING id::text`,
		rewarded[0], "cross-reversal-"+rewarded[0]).Scan(&reversalID); err != nil {
		t.Fatalf("seed the reversal transaction: %v", err)
	}
	if _, err := harness.pool.Exec(ctx, `UPDATE referral_rewards
		SET reversed_at = now(), reversal_ledger_transaction_id = $2::uuid,
		    reversal_reason = 'abuse review'
		WHERE id = $1::uuid`, rewardIDs[0], reversalID); err != nil {
		t.Fatalf("reverse the reward: %v", err)
	}

	// The invitation itself still happened, so invited and qualified are
	// unchanged; the money and the cap slot come back.
	assertAgreement("after one reward is reversed", 3, 2, 20000, 2)
}

// TestCrossSurfaceWithdrawnNewsDisappearsFromBoth is the visibility pair.
//
// Taking a post down does not clear `published_at` — deliberately, so
// republishing does not rewrite when customers first saw it — which means a
// surface that filtered on the timestamp alone would keep showing something an
// operator has already withdrawn. Both surfaces check the status, and this pins
// it: the two inboxes must not disagree about what has been taken down.
func TestCrossSurfaceWithdrawnNewsDisappearsFromBoth(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	support := newAccountSupport(t, harness)
	botStore, _ := crossSurfaceBot(ctx, t, harness, commercepg.New(harness.pool, nil, crossSurfaceOptions()))

	customerID := harness.customer(ctx, t)
	live := harness.newsPost(ctx, t, "cross-live-notes", "published")
	withdrawn := harness.newsPost(ctx, t, "cross-withdrawn-notes", "unpublished")
	archived := harness.newsPost(ctx, t, "cross-archived-notes", "archived")

	inboxes := func(stage string) ([]string, []string) {
		t.Helper()
		botItems, err := botStore.News(ctx, customerID, botapp.Locale("en"), 20)
		if err != nil {
			t.Fatalf("read the bot inbox %s: %v", stage, err)
		}
		webPage, err := support.News(ctx, customerID, "en", "", 20)
		if err != nil {
			t.Fatalf("read the web inbox %s: %v", stage, err)
		}
		botIDs := make([]string, 0, len(botItems))
		for _, item := range botItems {
			botIDs = append(botIDs, item.ID)
		}
		webIDs := make([]string, 0, len(webPage.Items))
		for _, item := range webPage.Items {
			webIDs = append(webIDs, item.ID)
		}
		if len(botIDs) != len(webIDs) {
			t.Fatalf("%s: the bot shows %d posts and the web shows %d", stage, len(botIDs), len(webIDs))
		}
		for index := range botIDs {
			if botIDs[index] != webIDs[index] {
				t.Fatalf("%s: position %d is %s in the bot and %s on the web",
					stage, index, botIDs[index], webIDs[index])
			}
		}
		botUnread, err := botStore.UnreadNewsCount(ctx, customerID, botapp.Locale("en"))
		if err != nil {
			t.Fatalf("count unread in the bot %s: %v", stage, err)
		}
		if botUnread != webPage.Unread {
			t.Fatalf("%s: the bot badge reads %d and the web badge reads %d",
				stage, botUnread, webPage.Unread)
		}
		return botIDs, webIDs
	}

	visible, _ := inboxes("with one published post")
	if len(visible) != 1 || visible[0] != live {
		t.Fatalf("the inbox contains %v, want only the published post %s", visible, live)
	}

	// A post nobody may read is not addressable either, and both surfaces refuse
	// it the same way rather than one of them silently accepting the identifier.
	for _, hidden := range []string{withdrawn, archived} {
		if _, err := botStore.NewsItem(ctx, customerID, hidden, botapp.Locale("en")); !errors.Is(err, botapp.ErrNewsNotFound) {
			t.Fatalf("the bot opened a withdrawn post %s: %v", hidden, err)
		}
		if err := support.MarkNewsRead(ctx, customerID, hidden); !errors.Is(err, accountsupport.ErrNotFound) {
			t.Fatalf("the web marked a withdrawn post %s read: %v", hidden, err)
		}
	}

	// Reading it on one surface clears the badge on the other, so the counts the
	// comparison above checks are counts of the same thing.
	if err := support.MarkNewsRead(ctx, customerID, live); err != nil {
		t.Fatalf("mark the post read on the web: %v", err)
	}
	inboxes("after the web read the post")

	// Now the operator withdraws the one post that was live. It has to leave both
	// inboxes, and it has to leave them together.
	if _, err := harness.pool.Exec(ctx,
		`UPDATE news_posts SET status = 'unpublished', unpublished_at = now() WHERE id = $1::uuid`,
		live); err != nil {
		t.Fatalf("withdraw the post: %v", err)
	}
	remaining, _ := inboxes("after the post was withdrawn")
	if len(remaining) != 0 {
		t.Fatalf("a withdrawn post is still readable: %v", remaining)
	}
	if _, err := botStore.NewsItem(ctx, customerID, live, botapp.Locale("en")); !errors.Is(err, botapp.ErrNewsNotFound) {
		t.Fatalf("the bot still opens the withdrawn post: %v", err)
	}

	// Archiving is the other way a post is taken down, and it is not a softer one.
	if _, err := harness.pool.Exec(ctx,
		`UPDATE news_posts SET status = 'archived', archived_at = now() WHERE id = $1::uuid`,
		live); err != nil {
		t.Fatalf("archive the post: %v", err)
	}
	if archivedInbox, _ := inboxes("after the post was archived"); len(archivedInbox) != 0 {
		t.Fatalf("an archived post is still readable: %v", archivedInbox)
	}
}
