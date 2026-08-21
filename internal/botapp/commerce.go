package botapp

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// CommerceSettings are the installation-wide knobs the checkout flow needs.
type CommerceSettings struct {
	// Currency is the settlement currency plans are compared in.
	Currency string
	// PublicURL is the base for provider return links.
	PublicURL string
	TermsURL  string
	// RecoveryWindow is how long an expired subscription keeps its one-tap
	// recovery offer.
	RecoveryWindow time.Duration
	// MinimumTrialAccountAge refuses trials from accounts created moments ago.
	MinimumTrialAccountAge time.Duration
	MarketingFrequencyCap  int
	MarketingWindow        time.Duration
	// CartTTL is how long a saved cart waits for the balance to cover it.
	CartTTL time.Duration
	// MultiSubscription mirrors the installation switch so a screen can hide the
	// subscription picker entirely when only one subscription is possible.
	MultiSubscription bool
}

// Commerce ties the bot to the commerce backend.
//
// The purchase flow itself — pricing a checkout, validating a promo code,
// creating the order, starting the payment — lives in internal/accountcheckout,
// which the web panel calls too. What remains here is the bot's own vocabulary
// around it: Telegram identifiers, Stars receipts, saved carts, and the screens
// that read them. Every method is still safe to call again with the same inputs.
type Commerce struct {
	logger   *slog.Logger
	store    *PostgresStore
	orders   *commercepg.Store
	payments *paymentservice.Service
	settings CommerceSettings
	// checkout is the shared customer checkout. Both surfaces price and confirm
	// through it, so a quote shown in a chat and one shown in a browser are the
	// same call against the same catalogue row.
	checkout *accountcheckout.Service
	// goods holds the digital-goods adapters. A nil value leaves the shop
	// unavailable, which is what an installation that sells none gets.
	goods GoodsProviders
	clock func() time.Time
}

// NewCommerce wires the bot's commerce surface.
func NewCommerce(logger *slog.Logger, store *PostgresStore, orders *commercepg.Store, payments *paymentservice.Service, settings CommerceSettings) *Commerce {
	checkout, err := accountcheckout.New(store.pool, orders, payments, accountcheckout.Options{
		Logger: logger,
		Settings: accountcheckout.Settings{
			Currency: settings.Currency, PublicURL: settings.PublicURL, TermsURL: settings.TermsURL,
			RecoveryWindow: settings.RecoveryWindow, MinimumTrialAccountAge: settings.MinimumTrialAccountAge,
			MultiSubscription: settings.MultiSubscription,
		},
	})
	if err != nil {
		// The shared checkout only refuses a nil pool or a nil commerce store, and
		// this constructor has already dereferenced the first and is handed the
		// second. Recording it keeps the failure visible if that ever stops being
		// true, rather than letting it surface as a crash mid-purchase.
		logger.Error("shared checkout could not be built", "error", err)
	}
	return &Commerce{
		logger: logger, store: store, orders: orders, payments: payments,
		settings: settings, checkout: checkout, clock: time.Now,
	}
}

// PaymentChoice is one offered payment method together with the currency and
// price that method would charge.
// CancelOrder cancels a customer's own unpaid order from the chat. It goes
// through the shared checkout service so the provider payment is withdrawn and
// a subscription row the order opened is released, exactly as on the web.
func (service *Commerce) CancelOrder(ctx context.Context, customerID, orderID string) error {
	return service.checkout.CancelOrderFrom(ctx, customerID, orderID, "cancelled by the customer in Telegram")
}

type PaymentChoice = accountcheckout.PaymentChoice

// PaymentHandle is the customer-facing result of starting a payment.
type PaymentHandle = accountcheckout.PaymentHandle

// PaymentChoices lists the methods that can actually settle a plan.
func (service *Commerce) PaymentChoices(ctx context.Context, planVersionID string) ([]PaymentChoice, error) {
	return service.checkout.PaymentChoices(ctx, planVersionID)
}

// Quote prices the open checkout. A refused promotion is reported through the
// quote instead of failing the screen, so the customer can remove or replace it.
func (service *Commerce) Quote(ctx context.Context, session CheckoutSession) (commerce.CheckoutQuote, error) {
	return service.checkout.Quote(ctx, session)
}

// ApplyPromo validates a promo code against the open checkout and stores either
// the accepted code or the reason it was refused.
func (service *Commerce) ApplyPromo(ctx context.Context, session CheckoutSession, code string) (CheckoutSession, error) {
	return service.checkout.ApplyPromo(ctx, session, code)
}

// Confirm turns the open checkout into an order. The checkout's idempotency key
// is reused, so a duplicate confirmation resolves to the order already created.
//
// The plan and the customer are no longer read from the caller: the trial gate
// and the order input are both derived from the checkout itself, so the bot and
// the web cannot confirm the same session on different terms.
func (service *Commerce) Confirm(ctx context.Context, session CheckoutSession, plan Plan, customer Customer) (string, error) {
	_, _ = plan, customer
	return service.checkout.Confirm(ctx, session)
}

// StartPayment creates or resumes the provider payment for an order. Stars
// invoices carry the paying Telegram account as receipt metadata because
// refundStarPayment cannot be issued without it.
func (service *Commerce) StartPayment(ctx context.Context, order OrderSummary, provider string, telegramID int64, description string) (PaymentHandle, error) {
	return service.checkout.StartPayment(ctx, accountcheckout.PaymentRequest{
		OrderID: order.ID, Provider: provider, Description: description,
		Channel: "telegram_bot", TelegramID: telegramID,
	})
}

// Refresh re-reads a provider payment so a customer who returns before the
// webhook arrives still sees the settled state.
func (service *Commerce) Refresh(ctx context.Context, order OrderSummary) {
	service.checkout.Refresh(ctx, order)
}

// SettleStars applies a Telegram Stars payment received on the authenticated
// update stream. Referral rewards are granted inside the settlement transaction
// itself, so nothing further is required here.
func (service *Commerce) SettleStars(ctx context.Context, orderID, chargeID string, amountMinor int64, updateID int64) error {
	_, err := service.payments.SettleTelegramStars(ctx, paymentservice.StarsSettlement{
		OrderID: orderID, ChargeID: chargeID, AmountMinor: amountMinor, UpdateID: updateID,
	})
	return err
}

// uuidText renders a database UUID as the canonical lowercase string form.
func uuidText(value pgtype.UUID) string { return accountcheckout.UUIDText(value) }

// preferredCurrency picks the settlement currency for one adapter. The rule
// itself is shared, so the currency shown beside a method in a chat is the one
// the browser shows beside the same method.
func preferredCurrency(option commerce.PaymentOption, available []string, preferred string) string {
	return accountcheckout.PreferredCurrency(option, available, preferred)
}
