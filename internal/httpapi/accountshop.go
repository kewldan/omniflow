package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/accountshop"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// mountShop registers the digital-goods routes on the authenticated customer
// router.
//
// The shop is absent rather than empty when this installation cannot sell.
// Nothing mounts without a service and a provider registry behind it, and the
// catalogue itself answers `shop_unavailable` rather than an empty list when
// there is nothing to sell through the registry that is attached. Both say the
// same thing: an empty catalogue reads as "sold out" — a temporary state a
// customer would come back for — where the truth is "not offered here".
func (handlers *AccountHandlers) mountShop(router chi.Router) {
	if handlers.shop == nil || !handlers.shop.Enabled() {
		return
	}
	router.Get("/shop/products", handlers.shopProducts)
	router.Get("/shop/products/{productID}", handlers.shopProductDetail)
	router.Post("/shop/recipient", handlers.shopReviewRecipient)
	router.Post("/shop/purchase", handlers.shopPurchase)
	router.Get("/shop/orders", handlers.shopOrders)
	router.Get("/shop/orders/{orderID}", handlers.shopOrder)
}

// shopProducts lists what this installation sells.
//
// No price is quoted here. A quote is a promise with an expiry attached, and
// issuing one per row for a screen nobody has chosen anything on would mean a
// page full of promises that start expiring while it is being read.
func (handlers *AccountHandlers) shopProducts(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	products, err := handlers.shop.Products(request.Context(), shopLocale(request, principal.Customer.Locale))
	if handlers.writeShopError(writer, request, err) {
		return
	}
	items := make([]map[string]any, 0, len(products))
	for _, product := range products {
		items = append(items, shopProductPayload(product))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

// shopProductDetail quotes the product the customer just opened.
func (handlers *AccountHandlers) shopProductDetail(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	quantity, ok := shopQuantity(writer, request)
	if !ok {
		return
	}
	detail, err := handlers.shop.Detail(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "productID"),
		shopLocale(request, principal.Customer.Locale), quantity, trimmedQuery(request, "promoCode"),
	)
	if handlers.writeShopError(writer, request, err) {
		return
	}

	payload := shopProductPayload(detail.Product)
	payload["quantity"] = detail.Quantity
	// The quote and its expiry are one object, so a client cannot read the
	// price without also being handed the moment it stops applying.
	payload["quote"] = map[string]any{
		"priceMinor": detail.Quote.PriceMinor,
		"currency":   detail.Quote.Currency,
		"expiresAt":  detail.Quote.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if detail.Promo.Code != "" {
		promo := map[string]any{"code": detail.Promo.Code, "discountMinor": detail.Promo.DiscountMinor}
		if detail.Promo.Rejection != "" {
			promo["rejection"] = detail.Promo.Rejection
		}
		payload["promo"] = promo
	}
	writeJSON(writer, http.StatusOK, payload)
}

// shopReviewRecipient normalises a handle and hands it back for confirmation.
//
// It deliberately changes nothing. The review step exists so the customer sees
// the exact string the gateway will be given before anybody is charged, and a
// step that also acted would defeat that.
func (handlers *AccountHandlers) shopReviewRecipient(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Recipient string `json:"recipient"`
		ProductID string `json:"productId"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	recipient, err := handlers.shop.Review(request.Context(), body.ProductID, body.Recipient)
	if handlers.writeShopError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"recipient": recipient.Username, "checked": recipient.Checked,
	})
}

// shopPurchase opens the order for a reviewed recipient against a live quote.
func (handlers *AccountHandlers) shopPurchase(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		ProductID string `json:"productId"`
		Quantity  int    `json:"quantity"`
		Recipient string `json:"recipient"`
		ForSelf   bool   `json:"forSelf"`
		// The quote the panel displayed, echoed back so the server can refuse to
		// charge a number the customer never saw.
		Quote struct {
			PriceMinor int64  `json:"priceMinor"`
			Currency   string `json:"currency"`
			ExpiresAt  string `json:"expiresAt"`
		} `json:"quote"`
		PromoCode string `json:"promoCode"`
		// UseWallet defaults to false in JSON, so the panel states it explicitly
		// rather than having a customer's balance spent by omission.
		UseWallet bool `json:"useWallet"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}

	key := request.Header.Get("Idempotency-Key")
	if !validIdempotencyKey(key) {
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"idempotency_key_required", "A purchase requires an Idempotency-Key header",
		)
		return
	}
	expiresAt := time.Time{}
	if body.Quote.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, body.Quote.ExpiresAt)
		if err != nil {
			writeProblem(
				writer, request, http.StatusBadRequest,
				"invalid_quote", "The quote expiry is not a timestamp",
			)
			return
		}
		expiresAt = parsed
	}

	order, err := handlers.shop.Purchase(request.Context(), accountshop.PurchaseRequest{
		CustomerID: principal.Customer.ID, ProductID: body.ProductID, Quantity: body.Quantity,
		Recipient: body.Recipient, ForSelf: body.ForSelf,
		ShownPriceMinor: body.Quote.PriceMinor, ShownCurrency: body.Quote.Currency,
		QuoteExpiresAt: expiresAt, PromoCode: body.PromoCode, UseWallet: body.UseWallet,
		IdempotencyKey: key, Locale: shopLocale(request, principal.Customer.Locale),
	})
	if handlers.writeShopError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusCreated, shopOrderPayload(order))
}

func (handlers *AccountHandlers) shopOrders(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	cursor := accountshop.Cursor{ID: trimmedQuery(request, "cursorId")}
	if raw := trimmedQuery(request, "cursor"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeProblem(writer, request, http.StatusBadRequest, "invalid_cursor", "The cursor is not a timestamp")
			return
		}
		cursor.CreatedAt = parsed
	}
	limit, _ := strconv.Atoi(trimmedQuery(request, "limit"))

	page, err := handlers.shop.Orders(
		request.Context(), principal.Customer.ID,
		shopLocale(request, principal.Customer.Locale), cursor, limit,
	)
	if handlers.writeShopError(writer, request, err) {
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, order := range page.Items {
		items = append(items, shopOrderPayload(order))
	}
	payload := map[string]any{"items": items}
	if page.HasMore {
		payload["nextCursor"] = page.Next.CreatedAt.UTC().Format(time.RFC3339Nano)
		payload["nextCursorId"] = page.Next.ID
	}
	writeJSON(writer, http.StatusOK, payload)
}

func (handlers *AccountHandlers) shopOrder(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	order, err := handlers.shop.Order(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "orderID"),
		shopLocale(request, principal.Customer.Locale),
	)
	if handlers.writeShopError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, shopOrderPayload(order))
}

// shopProductPayload is the wire shape of one catalogue entry.
//
// The operator's markup, the provider's cost, and the gateway's slug are all
// absent. None of them is the customer's business, and a field that is never
// serialised cannot be leaked by a screen that forgot.
func shopProductPayload(product accountshop.Product) map[string]any {
	payload := map[string]any{
		"id": product.ID, "code": product.Code, "kind": product.Kind,
		"name": product.Name, "description": product.Description,
		"currency": product.Currency, "available": product.Available,
		// priceKnown says whether `price` is a real published number or absent
		// pending a quote, so a screen never has to read a missing price as zero.
		"priceKnown": product.PriceKnown,
	}
	if product.PriceKnown {
		payload["priceMinor"] = product.PriceMinor
	}
	switch product.Kind {
	case "telegram_premium":
		payload["durationMonths"] = product.DurationMonths
	case "telegram_stars":
		payload["starQuantity"] = product.StarQuantity
	}
	return payload
}

// shopOrderPayload is the wire shape of one purchase.
func shopOrderPayload(order accountshop.Order) map[string]any {
	delivery := map[string]any{
		"state":            string(order.Delivery.State),
		"attempts":         order.Delivery.Attempts,
		"supportHandoff":   order.Delivery.SupportHandoff,
		"supportReference": order.Delivery.SupportReference,
	}
	if order.Delivery.FailureReason != "" {
		delivery["failureReason"] = order.Delivery.FailureReason
	}
	if !order.Delivery.DeliveredAt.IsZero() {
		delivery["deliveredAt"] = order.Delivery.DeliveredAt.Format(time.RFC3339)
	}
	if !order.Delivery.UpdatedAt.IsZero() {
		delivery["updatedAt"] = order.Delivery.UpdatedAt.Format(time.RFC3339)
	}
	if order.Delivery.Refund.Refunded {
		delivery["refund"] = map[string]any{
			"amountMinor": order.Delivery.Refund.AmountMinor,
			"currency":    order.Delivery.Refund.Currency,
		}
	}

	payload := map[string]any{
		"id": order.ID, "productName": order.ProductName, "kind": order.Kind,
		"quantity": order.Quantity, "recipient": order.Recipient, "forSelf": order.RecipientIsSelf,
		"currency": order.Currency,
		"amounts": map[string]any{
			"priceMinor": order.PriceMinor, "discountMinor": order.DiscountMinor,
			"walletMinor": order.WalletMinor, "externalMinor": order.ExternalMinor,
			"paidMinor": order.PaidMinor,
		},
		"payment": map[string]any{
			"state": order.PaymentState, "required": order.PaymentRequired,
			"possible": order.PaymentPossible,
		},
		"delivery":  delivery,
		"createdAt": order.CreatedAt.Format(time.RFC3339),
		"updatedAt": order.UpdatedAt.Format(time.RFC3339),
	}
	switch order.Kind {
	case "telegram_premium":
		payload["durationMonths"] = order.DurationMonths
	case "telegram_stars":
		payload["starQuantity"] = order.StarQuantity
	}
	return payload
}

// shopLocale prefers an explicit request over the stored preference, so a
// customer reading the panel in one language does not get a catalogue in
// another.
func shopLocale(request *http.Request, stored string) string {
	if requested := trimmedQuery(request, "locale"); requested != "" {
		return requested
	}
	return stored
}

// shopQuantity reads the requested quantity, defaulting to one.
func shopQuantity(writer http.ResponseWriter, request *http.Request) (int, bool) {
	raw := trimmedQuery(request, "quantity")
	if raw == "" {
		return 1, true
	}
	quantity, err := strconv.Atoi(raw)
	if err != nil {
		writeProblem(writer, request, http.StatusBadRequest, "invalid_quantity", "The quantity is not a number")
		return 0, false
	}
	return quantity, true
}

// writeShopError maps the shop's refusals onto responses, reporting whether it
// wrote one.
//
// Each code is one the panel acts on differently, which is why they are not
// collapsed. `quote_expired` re-quotes silently; `price_changed` re-quotes and
// asks the customer to agree to the new number; `recipient_not_reviewed` sends
// them back through the review step. A single "bad request" would leave the
// panel guessing at all three.
func (handlers *AccountHandlers) writeShopError(
	writer http.ResponseWriter, request *http.Request, err error,
) bool {
	switch {
	case err == nil:
		return false

	case errors.Is(err, accountshop.ErrUnavailable):
		writeProblem(
			writer, request, http.StatusServiceUnavailable,
			"shop_unavailable", "The shop is not available right now",
		)
	case errors.Is(err, accountshop.ErrQuoteExpired):
		writeProblem(
			writer, request, http.StatusConflict,
			"quote_expired", "That price has expired; ask for a new one",
		)
	case errors.Is(err, accountshop.ErrPriceChanged):
		writeProblem(
			writer, request, http.StatusConflict,
			"price_changed", "The price changed; review the new one before buying",
		)
	case errors.Is(err, accountshop.ErrPriceUnavailable):
		writeProblem(
			writer, request, http.StatusServiceUnavailable,
			"price_unavailable", "This product cannot be priced right now",
		)
	case errors.Is(err, accountshop.ErrRecipientInvalid):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"recipient_invalid", "That is not a usable Telegram username",
		)
	case errors.Is(err, accountshop.ErrRecipientNotReviewed):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"recipient_not_reviewed", "Confirm the recipient before buying",
		)

	// A promotion that stopped applying refuses the purchase rather than
	// quietly charging the full price: the customer chose to buy at a
	// discounted number and is entitled to decide again without it.
	case errors.Is(err, commercepg.ErrPromoUnknown):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "promo_unknown", "That code is not recognised")
	case errors.Is(err, commercepg.ErrPromoExhausted):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "promo_exhausted", "That code has been used up")
	case errors.Is(err, commercepg.ErrPromoBelowCost):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"promo_below_cost", "That code cannot be applied to this product",
		)
	case errors.Is(err, commercepg.ErrPromoIneligible), errors.Is(err, commercepg.ErrPromoInvalid):
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"promo_ineligible", "That code does not apply to this purchase",
		)
	case errors.Is(err, commercepg.ErrMaintenance):
		writeProblem(
			writer, request, http.StatusServiceUnavailable,
			"maintenance_active", "Purchases are paused for maintenance",
		)

	// The shared sentinels keep their shared handling, with shop wording: a
	// product and an order are not subscriptions, and "not yours" and "does not
	// exist" are the same answer for both.
	case errors.Is(err, accountpg.ErrNotFound):
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That was not found")
	case errors.Is(err, accountpg.ErrInvalidInput):
		writeProblem(writer, request, http.StatusUnprocessableEntity, "invalid_input", "That request cannot be filled")

	default:
		// The recipient never reaches a log line. A failed purchase is
		// identified by its product and its cause, which is all an operator
		// needs to diagnose one.
		handlers.logger.Error("customer shop request failed", "error", err)
		writeProblem(writer, request, http.StatusInternalServerError, "request_failed", "Something went wrong")
	}
	return true
}
