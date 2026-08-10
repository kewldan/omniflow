package botapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	databaseutil "github.com/omniflow/omniflow/internal/database"
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
}

// Commerce ties the bot to the v0.3 commerce backend. Every method is safe to
// call again with the same inputs: order creation reuses the checkout's
// idempotency key and payment creation reuses the order's.
type Commerce struct {
	logger   *slog.Logger
	store    *PostgresStore
	orders   *commercepg.Store
	payments *paymentservice.Service
	settings CommerceSettings
	clock    func() time.Time
}

// NewCommerce wires the bot's commerce surface.
func NewCommerce(logger *slog.Logger, store *PostgresStore, orders *commercepg.Store, payments *paymentservice.Service, settings CommerceSettings) *Commerce {
	return &Commerce{logger: logger, store: store, orders: orders, payments: payments, settings: settings, clock: time.Now}
}

// PaymentChoice is one offered payment method together with the currency and
// price that method would charge.
type PaymentChoice struct {
	Provider    string
	Currency    string
	AmountMinor int64
	Recurring   bool
}

// PaymentChoices lists the methods that can actually settle a plan. A method is
// offered only when the operator configured it and the plan carries a price in
// a currency the method supports.
func (service *Commerce) PaymentChoices(ctx context.Context, planVersionID string) ([]PaymentChoice, error) {
	prices, err := service.store.PlanPrices(ctx, planVersionID)
	if err != nil {
		return nil, err
	}
	currencies := make([]string, 0, len(prices))
	for currency := range prices {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	choices := make([]PaymentChoice, 0, len(currencies))
	for _, option := range service.payments.Options() {
		currency := preferredCurrency(option, currencies, service.settings.Currency)
		if currency == "" {
			continue
		}
		choices = append(choices, PaymentChoice{Provider: option.Provider, Currency: currency, AmountMinor: prices[currency], Recurring: option.Recurring})
	}
	sort.SliceStable(choices, func(left, right int) bool { return choices[left].Provider < choices[right].Provider })
	return choices, nil
}

// preferredCurrency picks the settlement currency for one adapter, favouring the
// installation default so prices stay comparable across payment methods.
func preferredCurrency(option commerce.PaymentOption, available []string, preferred string) string {
	if option.Enabled && option.Supports(preferred) {
		for _, currency := range available {
			if currency == preferred {
				return currency
			}
		}
	}
	if !option.Enabled {
		return ""
	}
	for _, currency := range available {
		if option.Supports(currency) {
			return currency
		}
	}
	return ""
}

// Quote prices the open checkout. A refused promotion is reported through the
// quote instead of failing the screen, so the customer can remove or replace it.
func (service *Commerce) Quote(ctx context.Context, session CheckoutSession) (commerce.CheckoutQuote, error) {
	input := commercepg.CreateOrderInput{
		CustomerID: session.CustomerID, PlanVersionID: session.PlanVersionID,
		Currency: session.Currency, Operation: session.Operation,
		PromoCode: session.PromoCode, SkipWallet: !session.ApplyWallet,
		IdempotencyKey: session.IdempotencyKey,
	}
	preview, err := service.orders.PreviewOrder(ctx, input)
	if rejection := promoRejection(err); rejection != "" {
		// A code that stopped being valid must not break the screen: price the
		// order without it and report why it no longer applies.
		input.PromoCode = ""
		preview, err = service.orders.PreviewOrder(ctx, input)
		if err != nil {
			return commerce.CheckoutQuote{}, err
		}
		quote := quoteFrom(preview, session)
		quote.PromoCode, quote.PromoRejection = "", rejection
		return quote, nil
	}
	if err != nil {
		return commerce.CheckoutQuote{}, err
	}
	return quoteFrom(preview, session), nil
}

func quoteFrom(preview commercepg.OrderQuote, session CheckoutSession) commerce.CheckoutQuote {
	return commerce.CheckoutQuote{
		Subtotal:           commerce.Money{Amount: preview.Plan.AmountMinor, Currency: preview.Plan.Currency},
		DiscountMinor:      preview.DiscountMinor,
		WalletBalanceMinor: preview.WalletBalance,
		WalletAppliedMinor: preview.WalletMinor,
		ExternalMinor:      preview.ExternalMinor,
		PromoCode:          session.PromoCode,
		PromoRejection:     session.PromoRejection,
	}
}

// ApplyPromo validates a promo code against the open checkout and stores either
// the accepted code or the reason it was refused.
func (service *Commerce) ApplyPromo(ctx context.Context, session CheckoutSession, code string) (CheckoutSession, error) {
	normalized, err := commerce.NormalizePromoCode(code)
	if err != nil {
		return service.store.SetCheckoutPromo(ctx, session.ID, "", "promo_invalid")
	}
	candidate := session
	candidate.PromoCode = normalized
	if _, err = service.orders.PreviewOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: candidate.CustomerID, PlanVersionID: candidate.PlanVersionID,
		Currency: candidate.Currency, Operation: candidate.Operation,
		PromoCode: normalized, SkipWallet: !candidate.ApplyWallet,
		IdempotencyKey: candidate.IdempotencyKey,
	}); err != nil {
		reason := promoRejection(err)
		if reason == "" {
			return CheckoutSession{}, err
		}
		return service.store.SetCheckoutPromo(ctx, session.ID, "", reason)
	}
	return service.store.SetCheckoutPromo(ctx, session.ID, normalized, "")
}

// promoRejection maps a store error onto the stable reason shown to a customer,
// or an empty string when the failure was not about the promotion.
func promoRejection(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, commercepg.ErrPromoUnknown):
		return "promo_unknown"
	case errors.Is(err, commercepg.ErrPromoIneligible):
		return "promo_ineligible"
	case errors.Is(err, commercepg.ErrPromoExhausted):
		return "promo_exhausted"
	case errors.Is(err, commercepg.ErrPromoInvalid):
		return "promo_invalid"
	default:
		return ""
	}
}

// Confirm turns the open checkout into an order. The checkout's idempotency key
// is reused, so a duplicate confirmation resolves to the order already created.
func (service *Commerce) Confirm(ctx context.Context, session CheckoutSession, plan Plan, customer Customer) (string, error) {
	if plan.Kind == "trial" {
		request, err := service.store.TrialContext(ctx, session.CustomerID)
		if err != nil {
			return "", err
		}
		request.PlanKind, request.Rule = plan.Kind, plan.TrialRule
		request.MinimumAccountAge = service.settings.MinimumTrialAccountAge
		if reason, trialErr := commerce.EvaluateTrial(request); trialErr != nil {
			return "", fmt.Errorf("%w: %s", commerce.ErrTrialNotEligible, reason)
		}
	}
	order, err := service.orders.CreateOrder(ctx, commercepg.CreateOrderInput{
		CustomerID: session.CustomerID, PlanVersionID: session.PlanVersionID,
		Currency: session.Currency, Operation: session.Operation,
		PromoCode: session.PromoCode, SkipWallet: !session.ApplyWallet,
		IdempotencyKey: session.IdempotencyKey,
	})
	if err != nil {
		return "", err
	}
	orderID := uuidText(order.ID)
	if err = service.store.AttachCheckoutOrder(ctx, session.ID, orderID); err != nil {
		return "", err
	}
	_ = customer
	return orderID, nil
}

// StartPayment creates or resumes the provider payment for an order. Stars
// invoices carry the paying Telegram account as receipt metadata because
// refundStarPayment cannot be issued without it.
func (service *Commerce) StartPayment(ctx context.Context, order OrderSummary, provider string, telegramID int64, description string) (PaymentHandle, error) {
	if !service.payments.Enabled(provider) {
		return PaymentHandle{}, errors.New("payment provider is not enabled")
	}
	receipt := map[string]any{"channel": "telegram_bot"}
	if provider == "telegram_stars" {
		receipt["telegramUserId"] = telegramID
	}
	intent, err := service.payments.CreateIntent(ctx, paymentservice.CreateIntentInput{
		OrderID: order.ID, Provider: provider,
		// The order's own idempotency key would not distinguish two providers,
		// so the payment key is scoped to the order and the chosen adapter.
		IdempotencyKey:  "order-" + order.ID + "-" + provider,
		Description:     description,
		ReturnURL:       service.returnURL(order.ID),
		ReceiptMetadata: receipt,
	})
	if err != nil {
		return PaymentHandle{}, err
	}
	return PaymentHandle{
		ID: uuidText(intent.ID), Provider: intent.Provider, Status: intent.Status,
		AmountMinor: intent.AmountMinor, Currency: intent.Currency,
		CheckoutURL: intent.CheckoutUrl.String,
	}, nil
}

// PaymentHandle is the customer-facing result of starting a payment.
type PaymentHandle struct {
	ID          string
	Provider    string
	Status      string
	AmountMinor int64
	Currency    string
	CheckoutURL string
}

func (service *Commerce) returnURL(orderID string) string {
	if service.settings.PublicURL == "" {
		return ""
	}
	return strings.TrimRight(service.settings.PublicURL, "/") + "/orders/" + orderID
}

// Refresh re-reads a provider payment so a customer who returns before the
// webhook arrives still sees the settled state.
func (service *Commerce) Refresh(ctx context.Context, order OrderSummary) {
	if order.PaymentIntentID == "" || order.Phase != commerce.PaymentPhasePending && order.Phase != commerce.PaymentPhaseAwaitingAction {
		return
	}
	if _, err := service.payments.Reconcile(ctx, order.PaymentIntentID); err != nil {
		service.logger.Debug("payment reconciliation is unavailable", "provider", order.Provider, "error", err)
	}
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
func uuidText(value pgtype.UUID) string {
	rendered := databaseutil.UUIDStrings([]pgtype.UUID{value})
	if len(rendered) == 0 {
		return ""
	}
	return rendered[0]
}
