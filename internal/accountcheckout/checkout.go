package accountcheckout

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/paymentservice"
)

// PaymentChoice is one offered payment method together with the currency and
// price that method would charge.
type PaymentChoice struct {
	Provider    string
	Currency    string
	AmountMinor int64
	Recurring   bool
}

// PaymentChoices lists the methods that can actually settle a plan.
//
// A method is offered only when the operator configured it and the plan carries
// a price in a currency that method supports. Offering one that cannot settle
// would move the failure from a screen the customer can still act on to the
// moment after they committed.
func (service *Service) PaymentChoices(ctx context.Context, planVersionID string) ([]PaymentChoice, error) {
	prices, err := service.store.PlanPrices(ctx, planVersionID)
	if err != nil {
		return nil, err
	}
	return service.choicesFor(prices), nil
}

func (service *Service) choicesFor(prices map[string]int64) []PaymentChoice {
	currencies := make([]string, 0, len(prices))
	for currency := range prices {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	choices := make([]PaymentChoice, 0, len(currencies))
	if service.payments == nil {
		return choices
	}
	for _, option := range service.payments.Options() {
		currency := PreferredCurrency(option, currencies, service.settings.Currency)
		if currency == "" {
			continue
		}
		choices = append(choices, PaymentChoice{
			Provider: option.Provider, Currency: currency,
			AmountMinor: prices[currency], Recurring: option.Recurring,
		})
	}
	sort.SliceStable(choices, func(left, right int) bool {
		return choices[left].Provider < choices[right].Provider
	})
	return choices
}

// PreferredCurrency picks the settlement currency for one adapter, favouring the
// installation default so prices stay comparable across payment methods.
//
// It is a pure function of the adapter and the plan's price list, which is what
// lets both surfaces show the same currency beside the same method without
// either of them holding a copy of the rule.
func PreferredCurrency(option commerce.PaymentOption, available []string, preferred string) string {
	if !option.Enabled {
		return ""
	}
	if option.Supports(preferred) {
		for _, currency := range available {
			if currency == preferred {
				return currency
			}
		}
	}
	for _, currency := range available {
		if option.Supports(currency) {
			return currency
		}
	}
	return ""
}

// OrderInput projects an open checkout onto the store's order input.
//
// It is the single place either surface decides what a checkout means, so a
// preview, a promo re-validation, and the order that follows can never disagree
// about the plan, the currency, the wallet, or the add-ons being bought.
func (service *Service) OrderInput(ctx context.Context, session Session) (commercepg.CreateOrderInput, error) {
	addons, err := service.store.CheckoutAddons(ctx, session.ID)
	if err != nil {
		return commercepg.CreateOrderInput{}, err
	}
	selections := make([]commercepg.AddonSelection, 0, len(addons))
	for _, addon := range addons {
		selections = append(selections, commercepg.AddonSelection{
			AddonVersionID: addon.AddonVersionID, Quantity: addon.Quantity,
		})
	}
	return commercepg.CreateOrderInput{
		CustomerID: session.CustomerID, PlanVersionID: session.PlanVersionID,
		Currency: session.Currency, Operation: session.Operation,
		PromoCode: session.PromoCode, SkipWallet: !session.ApplyWallet,
		IdempotencyKey: session.IdempotencyKey, SubscriptionID: session.SubscriptionID,
		NewSubscription: session.NewSubscription, SelectedSquadIDs: session.SelectedSquadIDs,
		Addons: selections,
	}, nil
}

// Quote prices the open checkout.
//
// A refused promotion is reported through the quote instead of failing the
// screen, so the customer can remove or replace the code rather than losing the
// checkout they had already configured.
func (service *Service) Quote(ctx context.Context, session Session) (commerce.CheckoutQuote, error) {
	input, err := service.OrderInput(ctx, session)
	if err != nil {
		return commerce.CheckoutQuote{}, err
	}
	preview, err := service.orders.PreviewOrder(ctx, input)
	if rejection := PromoRejection(err); rejection != "" {
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

func quoteFrom(preview commercepg.OrderQuote, session Session) commerce.CheckoutQuote {
	return commerce.CheckoutQuote{
		Subtotal:           commerce.Money{Amount: preview.Plan.AmountMinor, Currency: preview.Plan.Currency},
		DiscountMinor:      preview.DiscountMinor,
		WalletBalanceMinor: preview.WalletBalance,
		WalletAppliedMinor: preview.WalletMinor,
		ExternalMinor:      preview.ExternalMinor,
		PromoCode:          session.PromoCode,
		PromoRejection:     session.PromoRejection,
		AddonMinor:         preview.AddonMinor,
	}
}

// ApplyPromo validates a promo code against the open checkout and stores either
// the accepted code or the reason it was refused.
func (service *Service) ApplyPromo(ctx context.Context, session Session, code string) (Session, error) {
	normalized, err := commerce.NormalizePromoCode(code)
	if err != nil {
		return service.store.SetCheckoutPromo(ctx, session.ID, "", PromoInvalid)
	}
	candidate := session
	candidate.PromoCode = normalized
	input, err := service.OrderInput(ctx, candidate)
	if err != nil {
		return Session{}, err
	}
	if _, err = service.orders.PreviewOrder(ctx, input); err != nil {
		reason := PromoRejection(err)
		if reason == "" {
			return Session{}, err
		}
		return service.store.SetCheckoutPromo(ctx, session.ID, "", reason)
	}
	return service.store.SetCheckoutPromo(ctx, session.ID, normalized, "")
}

// PromoRejection maps a store error onto the stable reason shown to a customer,
// or an empty string when the failure was not about the promotion.
//
// The reasons are values rather than sentences because both panels look up their
// own Russian and English copy for them; a sentence returned from here would
// arrive on screen untranslated.
func PromoRejection(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, commercepg.ErrPromoUnknown):
		return PromoUnknown
	case errors.Is(err, commercepg.ErrPromoIneligible):
		return PromoIneligible
	case errors.Is(err, commercepg.ErrPromoExhausted):
		return PromoExhausted
	case errors.Is(err, commercepg.ErrPromoInvalid):
		return PromoInvalid
	default:
		return ""
	}
}

// Confirm turns the open checkout into an order.
//
// The checkout's idempotency key is reused, so a duplicate confirmation — a
// second tap, a resubmitted form, a retry after a timeout — resolves to the
// order already created rather than charging the customer twice.
func (service *Service) Confirm(ctx context.Context, session Session) (string, error) {
	kind, rule, err := service.store.planKind(ctx, session.PlanVersionID)
	if err != nil {
		return "", err
	}
	if kind == "trial" {
		request, trialErr := service.store.TrialContext(ctx, session.CustomerID)
		if trialErr != nil {
			return "", trialErr
		}
		request.PlanKind, request.Rule = kind, rule
		request.MinimumAccountAge = service.settings.MinimumTrialAccountAge
		if reason, evalErr := commerce.EvaluateTrial(request); evalErr != nil {
			return "", fmt.Errorf("%w: %s", commerce.ErrTrialNotEligible, reason)
		}
	}
	input, err := service.OrderInput(ctx, session)
	if err != nil {
		return "", err
	}
	order, err := service.orders.CreateOrder(ctx, input)
	if err != nil {
		return "", err
	}
	orderID := UUIDText(order.ID)
	if err = service.store.AttachCheckoutOrder(ctx, session.ID, orderID); err != nil {
		return "", err
	}
	return orderID, nil
}

// PaymentRequest is one attempt to settle an order through a provider.
type PaymentRequest struct {
	OrderID  string
	Provider string
	// Description is what the provider shows on the payment page or invoice.
	Description string
	// Channel records which surface started the payment. It is receipt metadata
	// only; nothing downstream branches on it.
	Channel string
	// TelegramID is the paying Telegram account. The Stars adapter cannot issue a
	// refund without it, so it is recorded when a Stars payment is started from a
	// surface that knows the account.
	TelegramID int64
}

// PaymentHandle is the customer-facing result of starting a payment.
//
// It carries no provider credential and no secret: a checkout URL is a
// capability the customer is meant to follow, and everything else the provider
// returned stays in the payment intent.
type PaymentHandle struct {
	ID          string
	Provider    string
	Status      string
	AmountMinor int64
	Currency    string
	CheckoutURL string
	// Handoff is how the panel should present this payment: follow a hosted
	// page, open a Telegram invoice, or wait for an operator to confirm a manual
	// transfer. It is derived here so neither panel has to know which adapters
	// behave which way.
	Handoff string
}

// Payment handoff kinds.
const (
	HandoffHosted   = "hosted"
	HandoffTelegram = "telegram_invoice"
	HandoffManual   = "manual"
	HandoffNone     = "none"
)

// StartPayment creates or resumes the provider payment for an order.
//
// It is idempotent per order and provider: the key is scoped to both, because
// the order's own key would not distinguish a customer who abandoned one method
// and came back with another, and reusing it would bind the second attempt to
// the first adapter's intent.
func (service *Service) StartPayment(ctx context.Context, request PaymentRequest) (PaymentHandle, error) {
	if service.payments == nil || !service.payments.Enabled(request.Provider) {
		return PaymentHandle{}, ErrProviderUnavailable
	}
	receipt := map[string]any{"channel": request.Channel}
	if request.Provider == "telegram_stars" && request.TelegramID != 0 {
		receipt["telegramUserId"] = request.TelegramID
	}
	intent, err := service.payments.CreateIntent(ctx, paymentservice.CreateIntentInput{
		OrderID: request.OrderID, Provider: request.Provider,
		IdempotencyKey:  "order-" + request.OrderID + "-" + request.Provider,
		Description:     request.Description,
		ReturnURL:       service.returnURL(request.OrderID),
		ReceiptMetadata: receipt,
	})
	if err != nil {
		return PaymentHandle{}, err
	}
	handle := PaymentHandle{
		ID: UUIDText(intent.ID), Provider: intent.Provider, Status: intent.Status,
		AmountMinor: intent.AmountMinor, Currency: intent.Currency,
		CheckoutURL: intent.CheckoutUrl.String,
	}
	handle.Handoff = HandoffFor(handle.Provider, handle.CheckoutURL)
	return handle, nil
}

// HandoffFor classifies how a payment is completed from the customer's side, so
// neither panel has to know which adapters behave which way.
func HandoffFor(provider, checkoutURL string) string {
	switch {
	case provider == "manual":
		return HandoffManual
	case provider == "telegram_stars" && checkoutURL != "":
		return HandoffTelegram
	case checkoutURL != "":
		return HandoffHosted
	default:
		return HandoffNone
	}
}

func (service *Service) returnURL(orderID string) string {
	if service.settings.PublicURL == "" {
		return ""
	}
	return strings.TrimRight(service.settings.PublicURL, "/") + "/account/orders/" + orderID
}

// Refresh re-reads a provider payment so a customer who returns before the
// webhook arrives still sees the settled state.
//
// It is the recovery path for a delayed or lost notification, and it is
// deliberately best-effort: an adapter that cannot be polled leaves the order
// exactly as the webhook will eventually find it.
func (service *Service) Refresh(ctx context.Context, order OrderSummary) {
	if service.payments == nil || order.PaymentIntentID == "" {
		return
	}
	if order.Phase != commerce.PaymentPhasePending && order.Phase != commerce.PaymentPhaseAwaitingAction {
		return
	}
	if _, err := service.payments.Reconcile(ctx, order.PaymentIntentID); err != nil {
		service.logger.Debug("payment reconciliation is unavailable",
			"provider", order.Provider, "error", err)
	}
}

// UUIDText renders a database UUID as the canonical lowercase string form.
func UUIDText(value pgtype.UUID) string {
	rendered := databaseutil.UUIDStrings([]pgtype.UUID{value})
	if len(rendered) == 0 {
		return ""
	}
	return rendered[0]
}
