package accountcheckout

import (
	"context"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// CheckoutView is the whole confirmation screen in one response.
//
// It is assembled on the server rather than stitched together from several
// endpoints because every part of it constrains the others: the chosen provider
// fixes the currency, the currency fixes the price, the price and the wallet
// balance fix what is still owed, and a promo code can change all three. A panel
// that fetched those separately would render, briefly, a combination that no
// order could ever be created from.
type CheckoutView struct {
	ID            string
	PlanVersionID string
	Plan          PlanOffer
	Operation     string
	Currency      string
	Provider      string
	Providers     []PaymentChoice
	ApplyWallet   bool
	Quote         commerce.CheckoutQuote
	// PromoRejection is empty when no code was refused, and otherwise carries the
	// stable reason the panel explains in the customer's language.
	PromoRejection string
	SubscriptionID string
	// NewSubscription reports that confirming opens an additional subscription
	// rather than changing one that exists.
	NewSubscription bool
	Targets         []SubscriptionTarget
	// TargetRequired is true when the installation allows several subscriptions,
	// the customer holds more than one, and this flow has not been told which one
	// it acts on. The panel must ask rather than let the server guess.
	TargetRequired    bool
	MultiSubscription bool
	Squads            SquadOffer
	SelectedSquadIDs  []string
	// SquadSelection reports a server choice the customer has not finished.
	// While it is required the quote is withheld rather than the checkout
	// failing: a plan that asks the customer to choose servers has nothing to
	// price until they do, and the earlier behaviour — a 422 on every read —
	// left the screen unable to show the very control that would resolve it.
	SquadSelection SquadSelectionState
	Addons         []AddonOffer
	SelectedAddons []CheckoutAddon
	ExpiresAt      time.Time
	TermsURL       string
}

// OpenRequest is what the panel submits to start a checkout.
type OpenRequest struct {
	PlanVersionID string
	// Operation is the customer-facing intent: purchase, trial, recovery,
	// renewal, extension, upgrade, or downgrade. It is normalized by the domain
	// so the surface's vocabulary and the order's vocabulary stay separate.
	Operation string
	// SubscriptionID names the subscription this flow acts on. It is required for
	// a change once the customer holds more than one.
	SubscriptionID string
	// NewSubscription asks for an additional subscription rather than a change to
	// an existing one. Only a purchase can honour it.
	NewSubscription bool
}

// UpdateRequest is a partial edit to the open checkout.
//
// Every field is a pointer so "leave this alone" and "clear this" are different
// requests. A PATCH that omitted a field and one that set it to empty would
// otherwise be indistinguishable, and clearing a subscription target by accident
// is how a renewal silently becomes a second subscription.
type UpdateRequest struct {
	Provider        *string
	Currency        *string
	ApplyWallet     *bool
	SubscriptionID  *string
	NewSubscription *bool
	SquadIDs        *[]string
}

// Open starts a checkout, replacing whatever the customer had open.
func (service *Service) Open(
	ctx context.Context, customerID, locale string, request OpenRequest,
) (CheckoutView, error) {
	operation, err := commerce.NormalizeOperation(request.Operation)
	if err != nil {
		return CheckoutView{}, invalidInput("that is not a checkout operation")
	}
	// The plan is read before anything is written, so a stale catalogue link
	// fails with "this plan is gone" rather than by opening a checkout that can
	// never be priced.
	record, err := service.store.planRecord(ctx, request.PlanVersionID, locale, service.settings.Currency)
	if err != nil {
		return CheckoutView{}, err
	}
	if !commerce.AllowedOperation(operation, record.upgradePolicy, record.downgradePolicy) {
		return CheckoutView{}, invalidInput("this plan does not allow that change")
	}
	target, err := service.resolveTarget(ctx, customerID, locale, operation, request)
	if err != nil {
		return CheckoutView{}, err
	}
	session, err := service.store.OpenCheckout(
		ctx, customerID, request.PlanVersionID, operation, service.settings.Currency, target, nil,
	)
	if err != nil {
		return CheckoutView{}, err
	}
	return service.view(ctx, session, locale)
}

// resolveTarget decides which subscription a checkout acts on.
//
// An explicit identifier always wins and is checked against the customer's own
// subscriptions, so an identifier from a URL cannot address someone else's. With
// nothing explicit, a change targets the only subscription there is — and where
// there is more than one it refuses rather than choosing, because a renewal
// applied to the wrong subscription is not visible until it is too late.
func (service *Service) resolveTarget(
	ctx context.Context, customerID, locale, operation string, request OpenRequest,
) (string, error) {
	targets, err := service.store.SubscriptionTargets(ctx, customerID, locale)
	if err != nil {
		return "", err
	}
	if identifier := strings.TrimSpace(request.SubscriptionID); identifier != "" {
		for _, target := range targets {
			if target.ID == identifier {
				return identifier, nil
			}
		}
		return "", ErrOrderNotFound
	}
	if len(targets) == 0 {
		return "", nil
	}
	multi := service.orders.SubscriptionPolicy().MultiEnabled
	if commerce.TargetsNewSubscription(operation) && multi && request.NewSubscription {
		return "", nil
	}
	if multi && len(targets) > 1 {
		return "", ErrSubscriptionTargetRequired
	}
	// A single-subscription installation, or a customer who holds exactly one:
	// buying again acts on what they already have rather than opening a second
	// subscription they never asked for.
	return targets[0].ID, nil
}

// View reads the open checkout with a live quote. It reports ErrNoCheckout when
// the customer has nothing in progress, which the panel renders as the catalogue
// rather than as an error.
func (service *Service) View(ctx context.Context, customerID, locale string) (CheckoutView, error) {
	session, found, err := service.store.Checkout(ctx, customerID)
	if err != nil {
		return CheckoutView{}, err
	}
	if !found {
		return CheckoutView{}, ErrNoCheckout
	}
	return service.view(ctx, session, locale)
}

// Update applies a partial edit and returns the re-priced checkout.
func (service *Service) Update(
	ctx context.Context, customerID, locale string, request UpdateRequest,
) (CheckoutView, error) {
	session, found, err := service.store.Checkout(ctx, customerID)
	if err != nil {
		return CheckoutView{}, err
	}
	if !found {
		return CheckoutView{}, ErrNoCheckout
	}
	if request.Provider != nil {
		if session, err = service.chooseProvider(ctx, session, *request.Provider, request.Currency); err != nil {
			return CheckoutView{}, err
		}
	}
	if request.ApplyWallet != nil {
		if session, err = service.store.SetCheckoutWallet(ctx, session.ID, *request.ApplyWallet); err != nil {
			return CheckoutView{}, err
		}
	}
	if request.SubscriptionID != nil || request.NewSubscription != nil {
		target := session.SubscriptionID
		if request.SubscriptionID != nil {
			target = strings.TrimSpace(*request.SubscriptionID)
		}
		if request.NewSubscription != nil && *request.NewSubscription {
			target = ""
		}
		if target != "" {
			if err = service.assertOwnsSubscription(ctx, customerID, locale, target); err != nil {
				return CheckoutView{}, err
			}
		}
		if session, err = service.store.SetCheckoutSubscription(ctx, session.ID, target); err != nil {
			return CheckoutView{}, err
		}
	}
	if request.SquadIDs != nil {
		// Validated before it is stored. A set the plan could never accept —
		// a server it does not offer, more than it allows — is refused here
		// with the session untouched, so the customer keeps a checkout they
		// can still finish; an incomplete set is stored and reported through
		// the view, because the screen sends the whole set on every tap.
		offer, offerErr := service.store.PlanSquads(ctx, session.PlanVersionID, locale)
		if offerErr != nil {
			return CheckoutView{}, offerErr
		}
		if err = ValidateSquadEdit(offer, *request.SquadIDs); err != nil {
			return CheckoutView{}, err
		}
		if session, err = service.store.SetCheckoutSquads(ctx, session.ID, *request.SquadIDs); err != nil {
			return CheckoutView{}, err
		}
	}
	return service.view(ctx, session, locale)
}

// chooseProvider fixes the payment method and, with it, the order currency.
//
// The currency is not taken on trust: it must be one the plan is priced in and
// the adapter settles, which is exactly what PaymentChoices already computed for
// the screen the customer chose from.
func (service *Service) chooseProvider(
	ctx context.Context, session Session, provider string, currency *string,
) (Session, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return service.store.SetCheckoutProvider(ctx, session.ID, "", session.Currency)
	}
	choices, err := service.PaymentChoices(ctx, session.PlanVersionID)
	if err != nil {
		return Session{}, err
	}
	for _, choice := range choices {
		if choice.Provider != provider {
			continue
		}
		settlement := choice.Currency
		if currency != nil && strings.TrimSpace(*currency) != "" {
			requested := strings.ToUpper(strings.TrimSpace(*currency))
			if requested != choice.Currency {
				return Session{}, invalidInput("that payment method cannot settle in %s", requested)
			}
			settlement = requested
		}
		return service.store.SetCheckoutProvider(ctx, session.ID, provider, settlement)
	}
	return Session{}, ErrProviderUnavailable
}

func (service *Service) assertOwnsSubscription(ctx context.Context, customerID, locale, target string) error {
	targets, err := service.store.SubscriptionTargets(ctx, customerID, locale)
	if err != nil {
		return err
	}
	for _, candidate := range targets {
		if candidate.ID == target {
			return nil
		}
	}
	return ErrOrderNotFound
}

// Cancel discards the customer's unfinished checkout.
func (service *Service) Cancel(ctx context.Context, customerID string) error {
	return service.store.CancelCheckout(ctx, customerID)
}

// ApplyPromoCode records a code against the open checkout, accepted or refused.
func (service *Service) ApplyPromoCode(
	ctx context.Context, customerID, locale, code string,
) (CheckoutView, error) {
	session, found, err := service.store.Checkout(ctx, customerID)
	if err != nil {
		return CheckoutView{}, err
	}
	if !found {
		return CheckoutView{}, ErrNoCheckout
	}
	if session, err = service.ApplyPromo(ctx, session, code); err != nil {
		return CheckoutView{}, err
	}
	return service.view(ctx, session, locale)
}

// RemovePromoCode clears the code and any rejection recorded with it.
func (service *Service) RemovePromoCode(ctx context.Context, customerID, locale string) (CheckoutView, error) {
	session, found, err := service.store.Checkout(ctx, customerID)
	if err != nil {
		return CheckoutView{}, err
	}
	if !found {
		return CheckoutView{}, ErrNoCheckout
	}
	if session, err = service.store.SetCheckoutPromo(ctx, session.ID, "", ""); err != nil {
		return CheckoutView{}, err
	}
	return service.view(ctx, session, locale)
}

// ToggleAddon adds an add-on to the checkout or takes it back off.
func (service *Service) ToggleAddon(
	ctx context.Context, customerID, locale, addonVersionID string,
) (CheckoutView, error) {
	session, found, err := service.store.Checkout(ctx, customerID)
	if err != nil {
		return CheckoutView{}, err
	}
	if !found {
		return CheckoutView{}, ErrNoCheckout
	}
	// The add-on must be one this plan actually offers. Without the check an
	// identifier from anywhere in the catalogue could be attached, and the order
	// would refuse it only after the customer confirmed.
	offered, err := service.store.PlanAddons(ctx, session.PlanVersionID, locale, session.Currency)
	if err != nil {
		return CheckoutView{}, err
	}
	known := false
	for _, addon := range offered {
		if addon.AddonVersionID == addonVersionID {
			known = true
			break
		}
	}
	if !known {
		return CheckoutView{}, ErrPlanUnavailable
	}
	if err = service.store.ToggleCheckoutAddon(ctx, session.ID, addonVersionID); err != nil {
		return CheckoutView{}, err
	}
	return service.view(ctx, session, locale)
}

// ConfirmCheckout turns the open checkout into an order and returns it.
//
// Nothing is charged here. Starting the provider payment is a separate step, so
// a customer who closed the tab between the two finds an order waiting rather
// than a payment nobody can account for.
func (service *Service) ConfirmCheckout(ctx context.Context, customerID, locale string) (OrderSummary, error) {
	session, found, err := service.store.Checkout(ctx, customerID)
	if err != nil {
		return OrderSummary{}, err
	}
	if !found {
		return OrderSummary{}, ErrNoCheckout
	}
	orderID, err := service.Confirm(ctx, session)
	if err != nil {
		return OrderSummary{}, err
	}
	return service.store.Order(ctx, customerID, orderID, locale)
}

// view assembles the confirmation screen from one checkout session.
func (service *Service) view(ctx context.Context, session Session, locale string) (CheckoutView, error) {
	record, err := service.store.planRecord(ctx, session.PlanVersionID, locale, session.Currency)
	if err != nil {
		return CheckoutView{}, err
	}
	quote, err := service.Quote(ctx, session)
	var selection SquadSelectionState
	if reason, incomplete := SquadSelectionIncomplete(err); incomplete {
		// Not a failure of the checkout: the customer has not chosen servers
		// yet. The screen renders the configurator and no price.
		selection = SquadSelectionState{Required: true, Reason: reason}
		quote, err = incompleteQuote(session), nil
	}
	if err != nil {
		return CheckoutView{}, err
	}
	choices, err := service.PaymentChoices(ctx, session.PlanVersionID)
	if err != nil {
		return CheckoutView{}, err
	}
	squads, err := service.store.PlanSquads(ctx, session.PlanVersionID, locale)
	if err != nil {
		return CheckoutView{}, err
	}
	addons, err := service.store.PlanAddons(ctx, session.PlanVersionID, locale, session.Currency)
	if err != nil {
		return CheckoutView{}, err
	}
	selected, err := service.store.CheckoutAddons(ctx, session.ID)
	if err != nil {
		return CheckoutView{}, err
	}
	targets, err := service.store.SubscriptionTargets(ctx, session.CustomerID, locale)
	if err != nil {
		return CheckoutView{}, err
	}
	multi := service.orders.SubscriptionPolicy().MultiEnabled
	return CheckoutView{
		ID: session.ID, PlanVersionID: session.PlanVersionID, Plan: record.offer,
		Operation: session.Operation, Currency: session.Currency, Provider: session.Provider,
		Providers: choices, ApplyWallet: session.ApplyWallet, Quote: quote,
		PromoRejection: quote.PromoRejection,
		SubscriptionID: session.SubscriptionID, NewSubscription: session.NewSubscription,
		Targets: targets,
		TargetRequired: multi && len(targets) > 1 &&
			session.SubscriptionID == "" && !session.NewSubscription,
		MultiSubscription: multi,
		Squads:            squads, SelectedSquadIDs: session.SelectedSquadIDs,
		SquadSelection: selection,
		Addons:         addons, SelectedAddons: selected,
		ExpiresAt: session.ExpiresAt, TermsURL: service.settings.TermsURL,
	}, nil
}

// Order reads one of the customer's orders with its payment, refunds, and
// provisioning progress attached.
func (service *Service) Order(ctx context.Context, customerID, orderID, locale string) (OrderSummary, []RefundStatus, error) {
	order, err := service.store.Order(ctx, customerID, orderID, locale)
	if err != nil {
		return OrderSummary{}, nil, err
	}
	refunds, err := service.store.Refunds(ctx, customerID, orderID)
	if err != nil {
		return OrderSummary{}, nil, err
	}
	return order, refunds, nil
}

// Orders lists the customer's order history, newest first.
func (service *Service) Orders(
	ctx context.Context, customerID, locale string, cursor Cursor, limit int,
) ([]OrderSummary, error) {
	return service.store.Orders(ctx, customerID, locale, cursor, limit)
}

// RefreshOrder reconciles a late or missing webhook and returns what the order
// looks like afterwards.
func (service *Service) RefreshOrder(ctx context.Context, customerID, orderID, locale string) (OrderSummary, error) {
	order, err := service.store.Order(ctx, customerID, orderID, locale)
	if err != nil {
		return OrderSummary{}, err
	}
	service.Refresh(ctx, order)
	return service.store.Order(ctx, customerID, orderID, locale)
}

// CancelOrder cancels one of the customer's own unpaid orders.
func (service *Service) CancelOrder(ctx context.Context, customerID, orderID string) error {
	return service.store.CancelOrder(ctx, customerID, orderID, "cancelled by the customer in the web panel")
}

// StartOrderPayment starts or resumes a provider payment for one of the
// customer's own orders.
//
// The order is read through the ownership-scoped query first, so the payment
// service is only ever handed an order this customer may pay for.
func (service *Service) StartOrderPayment(
	ctx context.Context, customerID, orderID, locale, provider string,
) (PaymentHandle, error) {
	order, err := service.store.Order(ctx, customerID, orderID, locale)
	if err != nil {
		return PaymentHandle{}, err
	}
	if order.ExternalMinor == 0 {
		return PaymentHandle{}, ErrPaymentNotRequired
	}
	if provider == "" {
		provider = order.Provider
	}
	if provider == "" {
		return PaymentHandle{}, invalidInput("a payment method is required")
	}
	return service.StartPayment(ctx, PaymentRequest{
		OrderID: order.ID, Provider: provider, Description: order.PlanName, Channel: "customer_web",
	})
}
