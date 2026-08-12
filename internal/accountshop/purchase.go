package accountshop

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/commercepg"
)

// PurchaseRequest is one reviewed shop purchase.
//
// It carries the quote the customer was actually looking at, not just the
// product. Without that the server could only re-quote and charge whatever came
// back, which is the failure this flow exists to prevent: the number on the
// screen has to be the number charged, and the only way to know it still is, is
// to be told what it was.
type PurchaseRequest struct {
	CustomerID string
	ProductID  string
	Quantity   int
	// Recipient must be the normalised handle the review step returned. Raw
	// input is refused rather than normalised again, so a client that skipped
	// the review is told to review instead of delivering unconfirmed.
	Recipient string
	// ForSelf records that the customer says the handle is their own. Omniflow
	// stores no Telegram username for a web customer, so this cannot be derived
	// and is never trusted for anything: it annotates the order for the
	// customer's own history and decides nothing about delivery.
	ForSelf bool
	// ShownPriceMinor, ShownCurrency, and QuoteExpiresAt are the quote the panel
	// displayed. They are checked, never charged.
	ShownPriceMinor int64
	ShownCurrency   string
	QuoteExpiresAt  time.Time
	PromoCode       string
	// UseWallet applies the customer's balance, exactly as a plan purchase does.
	UseWallet bool
	// IdempotencyKey comes from the request header, so a double-submitted form
	// and a retried fetch reach the order that already exists rather than a
	// second one.
	IdempotencyKey string
	Locale         string
}

// Purchase creates the shop order for a reviewed recipient against a live
// quote.
//
// Nothing is delivered here and no provider is contacted beyond the re-quote.
// Delivery begins when the order settles, from the same pipeline the bot's
// purchases go through, so an abandoned or unpaid order can never reach a
// gateway.
func (service *Service) Purchase(ctx context.Context, request PurchaseRequest) (Order, error) {
	if !service.Enabled() {
		return Order{}, ErrUnavailable
	}
	quantity := normalizeQuantity(request.Quantity)
	if quantity == 0 {
		return Order{}, accountpg.ErrInvalidInput
	}
	if !validIdempotencyKey(request.IdempotencyKey) {
		return Order{}, accountpg.ErrInvalidInput
	}
	recipient, err := reviewedRecipient(request.Recipient)
	if err != nil {
		return Order{}, err
	}
	product, err := service.product(ctx, request.ProductID, request.Locale)
	if err != nil {
		return Order{}, err
	}
	if !product.Available {
		return Order{}, ErrUnavailable
	}

	// The live quote is taken first and is the only number that can be charged.
	// What the customer submitted is an assertion about what they were shown,
	// and it is checked against this rather than believed.
	fresh, err := service.quote(ctx, product, quantity)
	if err != nil {
		return Order{}, err
	}
	shown := Quote{
		PriceMinor: request.ShownPriceMinor,
		Currency:   strings.ToUpper(strings.TrimSpace(request.ShownCurrency)),
		ExpiresAt:  request.QuoteExpiresAt.UTC(),
	}
	if err = evaluateQuote(shown, fresh, service.now()); err != nil {
		return Order{}, err
	}

	order, err := service.orders.CreateGoodsOrder(ctx, commercepg.GoodsOrderInput{
		CustomerID: request.CustomerID, ProductID: product.ID, Quantity: quantity,
		Recipient: recipient, RecipientIsSelf: request.ForSelf,
		CostMinor: fresh.costMinor, PriceMinor: fresh.PriceMinor, CostKnown: fresh.costKnown,
		Currency: fresh.Currency, QuoteExpiresAt: fresh.ExpiresAt,
		SkipWallet: !request.UseWallet, PromoCode: strings.TrimSpace(request.PromoCode),
		// A fixed price is one the operator chose, so it carries no provider
		// cost floor for a discount to breach.
		OperatorPriced: product.fixedMinor != nil,
		// The header key is namespaced rather than used bare, so a customer
		// reusing a key they minted for another surface cannot collide with a
		// key this one derived.
		IdempotencyKey: "goods:web:" + strings.TrimSpace(request.IdempotencyKey),
	})
	switch {
	case errors.Is(err, commercepg.ErrQuoteExpired):
		// The store re-checks the expiry inside its own transaction. Reaching
		// here means the window closed between the check above and the write,
		// which is exactly the race the second check exists for.
		return Order{}, ErrQuoteExpired
	case err != nil:
		return Order{}, err
	}

	return service.Order(ctx, request.CustomerID, uuidText(order.ID), request.Locale)
}

// uuidText renders a stored identifier for the read that follows a write.
func uuidText(id pgtype.UUID) string {
	value, err := id.Value()
	if err != nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

// validIdempotencyKey bounds a customer-supplied key.
//
// The length floor is what stops "1" from being a key. Keys are scoped to one
// customer, so a short one cannot reach another person's order, but it can
// silently return this customer's *earlier* purchase instead of making the one
// they just asked for — which is a duplicate-suppression bug that looks like a
// vanished order.
func validIdempotencyKey(value string) bool {
	trimmed := strings.TrimSpace(value)
	return len(trimmed) >= 8 && len(trimmed) <= 128
}
