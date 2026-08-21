package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/accountcheckout"
	"github.com/omniflow/omniflow/internal/adtracking"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// mountCheckout registers the plan, checkout, order, and wallet routes on the
// authenticated customer router.
//
// A nil service leaves them unmounted rather than answering 503 on each, so an
// installation with no commerce configured presents a panel that simply has no
// checkout instead of one whose every purchase control fails when pressed.
func (handlers *AccountHandlers) mountCheckout(router chi.Router) {
	if handlers.checkout == nil {
		return
	}
	router.Get("/plans", handlers.listPlans)
	router.Get("/plans/{planVersionID}", handlers.planDetail)

	router.Get("/checkout", handlers.readCheckout)
	router.Post("/checkout", handlers.openCheckout)
	router.Patch("/checkout", handlers.updateCheckout)
	router.Delete("/checkout", handlers.cancelCheckout)
	router.Post("/checkout/promo", handlers.applyPromo)
	router.Delete("/checkout/promo", handlers.removePromo)
	router.Post("/checkout/addons/{addonVersionID}", handlers.toggleCheckoutAddon)
	router.Post("/checkout/confirm", handlers.confirmCheckout)

	// Where an order came from, for the operator's own advertising measurement.
	// The browser sends it once, against one order, and only when the visitor
	// agreed to measurement — see `internal/adtracking`.
	router.Post("/orders/{orderID}/attribution", handlers.recordAttribution)

	router.Get("/orders", handlers.listOrders)
	router.Get("/orders/{orderID}", handlers.readOrder)
	router.Post("/orders/{orderID}/payment", handlers.startOrderPayment)
	router.Post("/orders/{orderID}/refresh", handlers.refreshOrder)
	router.Post("/orders/{orderID}/cancel", handlers.cancelOrder)

	router.Get("/wallet", handlers.readWallet)
	router.Post("/wallet/top-up", handlers.startTopUp)
}

// recordAttribution attaches an advertising origin to one of the customer's own
// orders.
//
// It is a separate call rather than a field on the checkout because a checkout
// is created from the bot as well as from the browser, and the bot has no URL
// to have carried anything. Widening the purchase path with a field that is
// always empty on one of its two callers would put an advertising concern into
// the middle of buying something.
//
// An order that is not the caller's answers 404, the same as one that does not
// exist. The two are deliberately indistinguishable: a different answer would
// confirm that somebody else's order exists.
func (handlers *AccountHandlers) recordAttribution(
	writer http.ResponseWriter, request *http.Request,
) {
	var body adtracking.Attribution
	if !decodeJSON(writer, request, &body) {
		return
	}
	principal, _ := CustomerFrom(request.Context())
	err := handlers.checkout.RecordAttribution(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "orderID"), body)
	switch {
	case errors.Is(err, accountcheckout.ErrNotYourOrder):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "Order not found")
	case errors.Is(err, adtracking.ErrNoAttribution),
		errors.Is(err, adtracking.ErrMalformedClickID),
		errors.Is(err, adtracking.ErrUnknownClickSource):
		// Refused rather than silently dropped. The storefront only ever sends
		// this when it captured something, so a refusal means the two disagree
		// about what an advertising parameter looks like — which is worth
		// finding out from a response rather than from an empty report.
		writeProblem(writer, request, http.StatusUnprocessableEntity,
			"validation_failed", "No usable advertising parameters")
	case err != nil:
		handlers.writeCheckoutError(writer, request, err)
	default:
		writer.WriteHeader(http.StatusNoContent)
	}
}

// ---------------------------------------------------------------------------
// Plans
// ---------------------------------------------------------------------------

func (handlers *AccountHandlers) listPlans(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	plans, err := handlers.checkout.Plans(
		request.Context(), principal.Customer.ID, accountLocale(request, principal.Customer.Locale),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	items := make([]map[string]any, 0, len(plans))
	for _, plan := range plans {
		items = append(items, planPayload(plan))
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"items": items, "currency": handlers.checkout.Settings().Currency,
	})
}

func (handlers *AccountHandlers) planDetail(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	detail, err := handlers.checkout.Plan(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "planVersionID"),
		accountLocale(request, principal.Customer.Locale),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	payload := planPayload(detail.PlanOffer)
	payload["squads"] = squadPayload(detail.Squads)
	addons := make([]map[string]any, 0, len(detail.Addons))
	for _, addon := range detail.Addons {
		addons = append(addons, addonPayload(addon))
	}
	payload["addons"] = addons
	promotions := make([]map[string]any, 0, len(detail.Promotions))
	for _, promotion := range detail.Promotions {
		entry := map[string]any{
			"code": promotion.Code, "kind": promotion.Kind,
			"value": promotion.Value, "eligible": promotion.Eligible,
		}
		if promotion.Currency != "" {
			entry["currency"] = promotion.Currency
		}
		if !promotion.StartsAt.IsZero() {
			entry["startsAt"] = promotion.StartsAt.Format(time.RFC3339)
		}
		if !promotion.EndsAt.IsZero() {
			entry["endsAt"] = promotion.EndsAt.Format(time.RFC3339)
		}
		promotions = append(promotions, entry)
	}
	payload["promotions"] = promotions
	payload["termsUrl"] = detail.TermsURL
	writeJSON(writer, http.StatusOK, payload)
}

// planPayload is the wire shape of one comparable plan.
//
// The traffic allowance and device limit are nullable rather than zero-valued:
// "unlimited" and "none" are different offers, and a panel that had to guess
// which a zero meant would eventually advertise the wrong one.
func planPayload(plan accountcheckout.PlanOffer) map[string]any {
	payload := map[string]any{
		"planId": plan.PlanID, "planVersionId": plan.PlanVersionID,
		"code": plan.Code, "kind": plan.Kind,
		"name": plan.Name, "description": plan.Description, "sortOrder": plan.SortOrder,
		"billingPeriod": plan.BillingPeriod, "durationSeconds": int64(plan.Duration.Seconds()),
		"gracePeriodSeconds": int64(plan.GracePeriod.Seconds()),
		"price": map[string]any{
			"amountMinor": plan.AmountMinor, "currency": plan.Currency,
		},
		"recurringCapable":   plan.RecurringCapable,
		"configurableSquads": plan.ConfigurableSquads,
		"operations":         plan.Operations,
		"eligible":           plan.Eligible,
		"held":               plan.Held,
		"trafficAllowanceBytes": func() any {
			if plan.TrafficAllowanceBytes == nil {
				return nil
			}
			return *plan.TrafficAllowanceBytes
		}(),
		"deviceLimit": func() any {
			if plan.DeviceLimit == nil {
				return nil
			}
			return *plan.DeviceLimit
		}(),
	}
	if plan.Operations == nil {
		payload["operations"] = []string{}
	}
	if plan.IneligibleReason != "" {
		payload["ineligibleReason"] = plan.IneligibleReason
	}
	return payload
}

func squadPayload(offer accountcheckout.SquadOffer) map[string]any {
	offered := make([]map[string]any, 0, len(offer.Offered))
	for _, squad := range offer.Offered {
		offered = append(offered, map[string]any{"squadId": squad.SquadID, "label": squad.Label})
	}
	payload := map[string]any{
		"selection": offer.Selection, "minimum": offer.Minimum,
		"configurable": offer.Configurable(), "offered": offered,
	}
	if offer.Maximum != nil {
		payload["maximum"] = *offer.Maximum
	} else {
		payload["maximum"] = nil
	}
	return payload
}

func addonPayload(addon accountcheckout.AddonOffer) map[string]any {
	return map[string]any{
		"addonId": addon.AddonID, "addonVersionId": addon.AddonVersionID,
		"code": addon.Code, "kind": addon.Kind, "name": addon.Name, "description": addon.Description,
		"maxQuantity": addon.MaxQuantity, "proration": string(addon.Proration),
		"squadCount": addon.SquadCount,
		"price":      map[string]any{"amountMinor": addon.AmountMinor, "currency": addon.Currency},
		"trafficBytes": func() any {
			if addon.TrafficBytes == nil {
				return nil
			}
			return *addon.TrafficBytes
		}(),
		"deviceSlots": func() any {
			if addon.DeviceSlots == nil {
				return nil
			}
			return *addon.DeviceSlots
		}(),
	}
}

// ---------------------------------------------------------------------------
// Checkout
// ---------------------------------------------------------------------------

func (handlers *AccountHandlers) readCheckout(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	view, err := handlers.checkout.View(
		request.Context(), principal.Customer.ID, accountLocale(request, principal.Customer.Locale),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, checkoutPayload(view))
}

func (handlers *AccountHandlers) openCheckout(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		PlanVersionID   string `json:"planVersionId"`
		Operation       string `json:"operation"`
		SubscriptionID  string `json:"subscriptionId"`
		NewSubscription bool   `json:"newSubscription"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	view, err := handlers.checkout.Open(
		request.Context(), principal.Customer.ID, accountLocale(request, principal.Customer.Locale),
		accountcheckout.OpenRequest{
			PlanVersionID: body.PlanVersionID, Operation: body.Operation,
			SubscriptionID: body.SubscriptionID, NewSubscription: body.NewSubscription,
		},
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusCreated, checkoutPayload(view))
}

func (handlers *AccountHandlers) updateCheckout(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	// Pointers rather than values: an omitted field and a cleared field are
	// different requests, and clearing a subscription target by accident is how a
	// renewal silently becomes a second subscription.
	var body struct {
		Provider        *string   `json:"provider"`
		Currency        *string   `json:"currency"`
		ApplyWallet     *bool     `json:"applyWallet"`
		SubscriptionID  *string   `json:"subscriptionId"`
		NewSubscription *bool     `json:"newSubscription"`
		SquadIDs        *[]string `json:"squadIds"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	view, err := handlers.checkout.Update(
		request.Context(), principal.Customer.ID, accountLocale(request, principal.Customer.Locale),
		accountcheckout.UpdateRequest{
			Provider: body.Provider, Currency: body.Currency, ApplyWallet: body.ApplyWallet,
			SubscriptionID: body.SubscriptionID, NewSubscription: body.NewSubscription,
			SquadIDs: body.SquadIDs,
		},
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, checkoutPayload(view))
}

func (handlers *AccountHandlers) cancelCheckout(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	if handlers.writeCheckoutError(writer, request, handlers.checkout.Cancel(request.Context(), principal.Customer.ID)) {
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AccountHandlers) applyPromo(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		Code string `json:"code"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	// A refused code is a 200 carrying a rejection reason, not an error. The
	// checkout survives it, the customer can try another code, and the panel has
	// a stable value to look up Russian and English copy for.
	view, err := handlers.checkout.ApplyPromoCode(
		request.Context(), principal.Customer.ID,
		accountLocale(request, principal.Customer.Locale), body.Code,
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, checkoutPayload(view))
}

func (handlers *AccountHandlers) removePromo(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	view, err := handlers.checkout.RemovePromoCode(
		request.Context(), principal.Customer.ID, accountLocale(request, principal.Customer.Locale),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, checkoutPayload(view))
}

func (handlers *AccountHandlers) toggleCheckoutAddon(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	view, err := handlers.checkout.ToggleAddon(
		request.Context(), principal.Customer.ID,
		accountLocale(request, principal.Customer.Locale), chi.URLParam(request, "addonVersionID"),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, checkoutPayload(view))
}

func (handlers *AccountHandlers) confirmCheckout(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	// The header is required so a client that retries has said it means to. It is
	// not what makes the retry safe: the checkout's own stored key is, so two
	// different header values against one checkout still resolve to one order.
	if !requireIdempotencyKey(writer, request) {
		return
	}
	order, err := handlers.checkout.ConfirmCheckout(
		request.Context(), principal.Customer.ID, accountLocale(request, principal.Customer.Locale),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusCreated, handlers.orderPayload(request.Context(), order, nil))
}

// checkoutPayload is the whole confirmation screen.
//
// The breakdown is spelled out rather than reduced to a total: a customer who is
// told only what they owe cannot tell a promotion from a wallet application, and
// the two have very different consequences if either is wrong.
func checkoutPayload(view accountcheckout.CheckoutView) map[string]any {
	providers := make([]map[string]any, 0, len(view.Providers))
	for _, choice := range view.Providers {
		providers = append(providers, paymentChoicePayload(choice))
	}
	addons := make([]map[string]any, 0, len(view.Addons))
	for _, addon := range view.Addons {
		addons = append(addons, addonPayload(addon))
	}
	selected := make([]map[string]any, 0, len(view.SelectedAddons))
	for _, addon := range view.SelectedAddons {
		selected = append(selected, map[string]any{
			"addonVersionId": addon.AddonVersionID, "quantity": addon.Quantity,
		})
	}
	targets := make([]map[string]any, 0, len(view.Targets))
	for _, target := range view.Targets {
		entry := map[string]any{
			"id": target.ID, "slot": target.Slot, "label": target.Label,
			"plan": target.PlanName, "status": target.Status,
		}
		if !target.EndsAt.IsZero() {
			entry["endsAt"] = target.EndsAt.Format(time.RFC3339)
		}
		targets = append(targets, entry)
	}
	payload := map[string]any{
		"id": view.ID, "planVersionId": view.PlanVersionID, "plan": planPayload(view.Plan),
		"operation": view.Operation, "currency": view.Currency, "provider": view.Provider,
		"providers": providers, "applyWallet": view.ApplyWallet,
		"quote": map[string]any{
			"currency":           view.Quote.Subtotal.Currency,
			"subtotalMinor":      view.Quote.Subtotal.Amount,
			"addonMinor":         view.Quote.AddonMinor,
			"discountMinor":      view.Quote.DiscountMinor,
			"walletBalanceMinor": view.Quote.WalletBalanceMinor,
			"walletAppliedMinor": view.Quote.WalletAppliedMinor,
			"externalMinor":      view.Quote.ExternalMinor,
		},
		"promoCode":         view.Quote.PromoCode,
		"subscriptionId":    view.SubscriptionID,
		"newSubscription":   view.NewSubscription,
		"subscriptions":     targets,
		"targetRequired":    view.TargetRequired,
		"multiSubscription": view.MultiSubscription,
		"squads":            squadPayload(view.Squads),
		"selectedSquadIds":  view.SelectedSquadIDs,
		"squadSelection":    map[string]any{"required": view.SquadSelection.Required},
		"quoteAvailable":    !view.SquadSelection.Required,
		"addons":            addons,
		"selectedAddons":    selected,
		"termsUrl":          view.TermsURL,
	}
	if view.SelectedSquadIDs == nil {
		payload["selectedSquadIds"] = []string{}
	}
	if view.PromoRejection != "" {
		payload["promoRejection"] = view.PromoRejection
	}
	if view.SquadSelection.Required {
		payload["squadSelection"] = map[string]any{
			"required": true, "reason": view.SquadSelection.Reason,
		}
	}
	if !view.ExpiresAt.IsZero() {
		payload["expiresAt"] = view.ExpiresAt.Format(time.RFC3339)
	}
	return payload
}

// ---------------------------------------------------------------------------
// Orders
// ---------------------------------------------------------------------------

func (handlers *AccountHandlers) listOrders(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	cursor, ok := readCursor(writer, request)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(trimmedQuery(request, "limit"))
	orders, err := handlers.checkout.Orders(
		request.Context(), principal.Customer.ID,
		accountLocale(request, principal.Customer.Locale), cursor, limit,
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	items := make([]map[string]any, 0, len(orders))
	for _, order := range orders {
		items = append(items, handlers.orderPayload(request.Context(), order, nil))
	}
	payload := map[string]any{"items": items}
	// A cursor only when the page came back full. Publishing one for every
	// non-empty page told the panel that more existed after the last order the
	// customer had, which rendered a "load more" button that did nothing.
	if len(orders) > 0 && len(orders) == accountcheckout.BoundLimit(limit) {
		last := orders[len(orders)-1]
		payload["nextCursor"] = last.CreatedAt.Format(time.RFC3339Nano)
		payload["nextCursorId"] = last.ID
	}
	writeJSON(writer, http.StatusOK, payload)
}

func (handlers *AccountHandlers) readOrder(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	order, refunds, err := handlers.checkout.Order(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "orderID"),
		accountLocale(request, principal.Customer.Locale),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	payload := handlers.orderPayload(request.Context(), order, refunds)
	// A pending order with no payment yet needs a way to start one that does
	// not depend on the URL the checkout redirected to: the methods that can
	// settle this order, in its currency, and the one the checkout recorded so
	// the page can preselect it.
	if accountcheckout.OrderPayable(order, time.Now()) == nil {
		choices, choiceErr := handlers.checkout.OrderPaymentChoices(request.Context(), principal.Customer.ID, order)
		if handlers.writeCheckoutError(writer, request, choiceErr) {
			return
		}
		items := make([]map[string]any, 0, len(choices))
		for _, choice := range choices {
			items = append(items, paymentChoicePayload(choice))
		}
		payload["paymentChoices"] = items
		recorded, recordErr := handlers.checkout.Store().RecordedOrderProvider(
			request.Context(), principal.Customer.ID, order.ID,
		)
		if recordErr != nil {
			handlers.logger.Warn("recorded order provider could not be read", "error", recordErr)
		}
		if recorded != "" {
			payload["preferredProvider"] = recorded
		}
	}
	writeJSON(writer, http.StatusOK, payload)
}

func (handlers *AccountHandlers) startOrderPayment(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	if !requireIdempotencyKey(writer, request) {
		return
	}
	var body struct {
		Provider string `json:"provider"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	handle, err := handlers.checkout.StartOrderPayment(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "orderID"),
		accountLocale(request, principal.Customer.Locale), strings.TrimSpace(body.Provider),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, paymentPayload(handle))
}

func (handlers *AccountHandlers) refreshOrder(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	order, err := handlers.checkout.RefreshOrder(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "orderID"),
		accountLocale(request, principal.Customer.Locale),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, handlers.orderPayload(request.Context(), order, nil))
}

func (handlers *AccountHandlers) cancelOrder(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	err := handlers.checkout.CancelOrder(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "orderID"),
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// orderPayload is one order with its payment, provisioning, and refunds.
//
// Provisioning progress comes from the fulfillment operation the order created,
// so a refresh, a second tab, or a switch to Telegram shows the same state. It is
// never carried by the client: a page that remembered "we are setting this up"
// would forget it the moment it reloaded, which is exactly when the customer
// most wants to know.
//
// The payment handoff is decided by the checkout service rather than here,
// because a Telegram Stars payment has no URL of its own and its link is built
// from the bot's name — see accountcheckout.Handoff.
func (handlers *AccountHandlers) orderPayload(
	ctx context.Context, order accountcheckout.OrderSummary, refunds []accountcheckout.RefundStatus,
) map[string]any {
	handoff, checkoutURL := handlers.checkout.Handoff(ctx, order.Provider, order.CheckoutURL, order.ID)
	return orderPayloadWith(order, refunds, handoff, checkoutURL)
}

func orderPayloadWith(
	order accountcheckout.OrderSummary, refunds []accountcheckout.RefundStatus, handoff, checkoutURL string,
) map[string]any {
	payload := map[string]any{
		"id": order.ID, "state": string(order.State), "operation": order.Operation,
		"phase": string(order.Phase), "currency": order.Currency,
		"subtotalMinor": order.SubtotalMinor, "discountMinor": order.DiscountMinor,
		"walletMinor": order.WalletMinor, "externalMinor": order.ExternalMinor,
		"paidMinor": order.PaidMinor, "refundedMinor": order.RefundedMinor,
		"plan": order.PlanName, "createdAt": order.CreatedAt.Format(time.RFC3339),
		"subscriptionId": order.SubscriptionID,
	}
	if !order.ExpiresAt.IsZero() {
		payload["expiresAt"] = order.ExpiresAt.Format(time.RFC3339)
	}
	if order.PaymentIntentID != "" {
		payment := map[string]any{
			"id": order.PaymentIntentID, "provider": order.Provider, "status": order.PaymentStatus,
			"handoff": handoff,
		}
		if checkoutURL != "" {
			payment["checkoutUrl"] = checkoutURL
		}
		if order.ReceiptURL != "" {
			payment["receiptUrl"] = order.ReceiptURL
		}
		payload["payment"] = payment
	}
	if order.EntitlementID != "" || order.FulfillmentStatus != "" {
		fulfillment := map[string]any{
			"status": order.FulfillmentStatus, "attempts": order.FulfillmentAttempts,
		}
		if order.FulfillmentErrorCode != "" {
			fulfillment["errorCode"] = order.FulfillmentErrorCode
		}
		if !order.FulfillmentUpdatedAt.IsZero() {
			fulfillment["updatedAt"] = order.FulfillmentUpdatedAt.Format(time.RFC3339)
		}
		payload["fulfillment"] = fulfillment
	}
	if refunds != nil {
		items := make([]map[string]any, 0, len(refunds))
		for _, refund := range refunds {
			items = append(items, map[string]any{
				"status": refund.Status, "amountMinor": refund.AmountMinor,
				"currency": refund.Currency, "createdAt": refund.CreatedAt.Format(time.RFC3339),
			})
		}
		payload["refunds"] = items
	}
	return payload
}

// paymentChoicePayload is one offered method with the currency and price it
// would charge, the shape both the checkout and the order screens read.
func paymentChoicePayload(choice accountcheckout.PaymentChoice) map[string]any {
	return map[string]any{
		"provider": choice.Provider, "currency": choice.Currency,
		"amountMinor": choice.AmountMinor, "recurring": choice.Recurring,
	}
}

func paymentPayload(handle accountcheckout.PaymentHandle) map[string]any {
	payload := map[string]any{
		"id": handle.ID, "provider": handle.Provider, "status": handle.Status,
		"amountMinor": handle.AmountMinor, "currency": handle.Currency,
		"handoff": handle.Handoff,
	}
	if handle.CheckoutURL != "" {
		payload["checkoutUrl"] = handle.CheckoutURL
	}
	return payload
}

// ---------------------------------------------------------------------------
// Wallet
// ---------------------------------------------------------------------------

func (handlers *AccountHandlers) readWallet(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	view, err := handlers.checkout.Wallet(request.Context(), principal.Customer.ID)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	cursor, ok := readCursor(writer, request)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(trimmedQuery(request, "limit"))
	entries, err := handlers.checkout.Store().WalletHistory(
		request.Context(), principal.Customer.ID,
		strings.ToUpper(trimmedQuery(request, "currency")), cursor, limit,
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}

	balances := make([]map[string]any, 0, len(view.Balances))
	for _, balance := range view.Balances {
		balances = append(balances, map[string]any{
			"currency": balance.Currency, "totalMinor": balance.TotalMinor,
			"reservedMinor": balance.ReservedMinor, "availableMinor": balance.AvailableMinor,
		})
	}
	providers := make([]map[string]any, 0, len(view.Providers))
	for _, choice := range view.Providers {
		providers = append(providers, map[string]any{
			"provider": choice.Provider, "currency": choice.Currency, "recurring": choice.Recurring,
		})
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item := map[string]any{
			"id": entry.ID, "type": entry.Type, "amountMinor": entry.AmountMinor,
			"currency": entry.Currency, "occurredAt": entry.OccurredAt.Format(time.RFC3339),
		}
		if entry.Reason != "" {
			item["reason"] = entry.Reason
		}
		items = append(items, item)
	}
	payload := map[string]any{
		"balances": balances, "currency": view.Currency,
		"topUp": map[string]any{
			"enabled": view.TopUpEnabled, "minimumMinor": view.MinimumMinor,
			"maximumMinor": view.MaximumMinor, "presets": view.Presets,
			"remainingWindowMinor": view.RemainingWindowMinor, "providers": providers,
		},
		"entries": items,
	}
	// Same contract as the order list: a short page is the last one, so no cursor.
	if len(entries) > 0 && len(entries) == accountcheckout.BoundLimit(limit) {
		last := entries[len(entries)-1]
		payload["nextCursor"] = last.OccurredAt.Format(time.RFC3339Nano)
		payload["nextCursorId"] = last.ID
	}
	writeJSON(writer, http.StatusOK, payload)
}

func (handlers *AccountHandlers) startTopUp(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	// A top-up mints real money into the wallet, so the caller's key is the
	// order's key: a retried request credits the same top-up rather than a second.
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if !validIdempotencyKey(key) {
		writeProblem(
			writer, request, http.StatusBadRequest,
			"idempotency_key_required", "A top-up requires an Idempotency-Key header",
		)
		return
	}
	var body struct {
		AmountMinor int64  `json:"amountMinor"`
		Currency    string `json:"currency"`
		Provider    string `json:"provider"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	topUp, err := handlers.checkout.StartTopUp(
		request.Context(), principal.Customer.ID, strings.ToUpper(strings.TrimSpace(body.Currency)),
		body.AmountMinor, strings.TrimSpace(body.Provider), key,
	)
	if handlers.writeCheckoutError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{
		"orderId": topUp.OrderID, "currency": topUp.Currency,
		"amountMinor": topUp.AmountMinor, "state": topUp.State,
		"payment": paymentPayload(topUp.Payment),
	})
}

// ---------------------------------------------------------------------------
// Shared transport helpers
// ---------------------------------------------------------------------------

// accountLocale resolves the language a catalogue is read in, preferring an
// explicit request over the customer's stored preference so a language switch
// takes effect before the profile has been saved.
func accountLocale(request *http.Request, stored string) string {
	if requested := strings.ToLower(trimmedQuery(request, "locale")); requested == "ru" || requested == "en" {
		return requested
	}
	return stored
}

// readCursor parses the pagination position, reporting whether it wrote a
// problem response.
func readCursor(writer http.ResponseWriter, request *http.Request) (accountcheckout.Cursor, bool) {
	cursor := accountcheckout.Cursor{ID: trimmedQuery(request, "cursorId")}
	raw := trimmedQuery(request, "cursor")
	if raw == "" {
		return accountcheckout.Cursor{}, true
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_cursor", "The cursor is not a timestamp")
		return accountcheckout.Cursor{}, false
	}
	cursor.At = parsed
	return cursor, true
}

// requireIdempotencyKey enforces the header on a mutation that must be safe to
// retry, reporting whether it wrote a problem response.
func requireIdempotencyKey(writer http.ResponseWriter, request *http.Request) bool {
	if validIdempotencyKey(strings.TrimSpace(request.Header.Get("Idempotency-Key"))) {
		return true
	}
	writeProblem(
		writer, request, http.StatusBadRequest,
		"idempotency_key_required", "This action requires an Idempotency-Key header",
	)
	return false
}

// writeCheckoutError maps the checkout's errors onto responses, reporting
// whether it wrote one.
//
// Where the domain already carries a stable machine reason — a refused promo
// code, a rejected trial, a subscription limit — that reason becomes the problem
// code rather than being buried in prose. RFC 9457's `type` is what a client is
// supposed to branch on, and it is the only part of the response the panel can
// translate into Russian and English copy of its own.
func (handlers *AccountHandlers) writeCheckoutError(
	writer http.ResponseWriter, request *http.Request, err error,
) bool {
	switch {
	case err == nil:
		return false

	case errors.Is(err, accountcheckout.ErrNoCheckout):
		writeProblem(writer, request, http.StatusNotFound, "no_checkout", "No checkout is open")
	case errors.Is(err, accountcheckout.ErrCheckoutSettled):
		writeProblem(
			writer, request, http.StatusConflict,
			"checkout_settled", "This checkout has already become an order",
		)
	case errors.Is(err, accountcheckout.ErrOrderNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That order was not found")
	case errors.Is(err, accountcheckout.ErrPlanUnavailable),
		errors.Is(err, commercepg.ErrPlanUnavailable):
		writeProblem(
			writer, request, http.StatusNotFound,
			"plan_unavailable", "That plan is no longer available",
		)
	case errors.Is(err, accountcheckout.ErrProviderUnavailable):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"provider_unavailable", "That payment method is not available",
		)
	case errors.Is(err, accountcheckout.ErrOrderNotPayable):
		writeProblem(
			writer, request, http.StatusConflict,
			"order_not_payable", "This order can no longer be paid; reload it to see its state",
		)
	case errors.Is(err, accountcheckout.ErrProviderCurrency):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"provider_currency_unsupported", "That payment method cannot settle this order's currency",
		)
	case errors.Is(err, accountcheckout.ErrPaymentNotRequired):
		writeProblem(
			writer, request, http.StatusConflict,
			"payment_not_required", "This order has already been paid for",
		)
	case errors.Is(err, accountcheckout.ErrOrderNotCancellable):
		writeProblem(
			writer, request, http.StatusConflict,
			"order_not_cancellable", "Only an unpaid order can be cancelled",
		)
	case errors.Is(err, accountcheckout.ErrSubscriptionTargetRequired):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"subscription_target_required", "Choose which subscription this applies to",
		)

	case errors.Is(err, commercepg.ErrMaintenance):
		writeProblem(
			writer, request, http.StatusServiceUnavailable,
			"maintenance_active", "Purchases are paused while maintenance is in progress",
		)
	case errors.Is(err, commercepg.ErrOperationForbidden):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"operation_forbidden", "This plan does not allow that change",
		)
	case errors.Is(err, commercepg.ErrSubscriptionUnknown):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That subscription was not found")
	case errors.Is(err, commercepg.ErrAddonUnavailable):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"addon_unavailable", "That add-on is not available for this plan",
		)
	case errors.Is(err, commercepg.ErrNoActiveSubscription):
		writeProblem(
			writer, request, http.StatusConflict,
			"no_active_subscription", "There is no active subscription to change",
		)
	case errors.Is(err, commercepg.ErrTrialAlreadyClaimed):
		writeProblem(
			writer, request, http.StatusConflict,
			"trial_already_used", "A trial has already been used on this account",
		)

	case errors.Is(err, commerce.ErrTrialNotEligible):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"trial_"+lastReason(err, "not_eligible"), "This trial is not available for this account",
		)
	case errors.Is(err, commerce.ErrSubscriptionRejected):
		writeProblem(
			writer, request, http.StatusConflict,
			commerce.SubscriptionRejectionReason(err), "Another subscription cannot be opened",
		)
	case errors.Is(err, commerce.ErrSquadSelection):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			lastReason(err, commerce.SquadSelectionRefused), "That server selection is not allowed",
		)
	case errors.Is(err, commerce.ErrTopUpRejected):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			lastReason(err, commerce.TopUpInvalidAmount), "That top-up amount cannot be accepted",
		)

	case accountcheckout.PromoRejection(err) != "":
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			accountcheckout.PromoRejection(err), "That promo code cannot be applied",
		)

	default:
		// Anything left is either an accountpg sentinel the account handler
		// already knows how to answer, or a genuine failure it will log.
		return handlers.writeAccountError(writer, request, err)
	}
	return true
}

// lastReason recovers the stable machine reason a wrapped domain error carries
// after its final ": ", falling back when the error carried none.
func lastReason(err error, fallback string) string {
	message := err.Error()
	if index := strings.LastIndex(message, ": "); index >= 0 {
		if reason := message[index+2:]; reason != "" {
			return reason
		}
	}
	return fallback
}
