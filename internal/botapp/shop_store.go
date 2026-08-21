package botapp

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/goods"
)

// ShopProduct is one catalog entry as the customer sees it.
//
// `PriceMinor` is only meaningful once a quote has been taken: an operator who
// publishes a fixed price has one immediately, while a product priced off a
// live provider rate has none until the provider is asked. `PriceKnown` says
// which, so the catalog can show "from" pricing rather than a fabricated
// number.
type ShopProduct struct {
	ID             string
	Code           string
	Kind           string
	DurationMonths int
	StarQuantity   int
	Name           string
	Description    string
	Currency       string
	PriceMinor     int64
	PriceKnown     bool
	ProviderSlug   string
	QuoteTTL       time.Duration
	MarkupBPS      int
	Rounding       string
	FixedMinor     *int64
}

// ShopProducts lists the visible catalog in the customer's language.
//
// A product with no localization for the requested locale falls back to its
// code rather than being hidden: an operator who added a product and has not
// translated it yet should see it in the bot and notice, not have it silently
// disappear for half their customers.
func (store *PostgresStore) ShopProducts(
	ctx context.Context, locale Locale,
) ([]ShopProduct, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT p.id::text, p.code, p.provider_slug, p.kind,
		       COALESCE(p.duration_months, 0), COALESCE(p.star_quantity, 0),
		       COALESCE(l.name, p.code), COALESCE(l.description, ''),
		       r.currency, r.markup_bps, r.rounding, r.fixed_amount_minor, r.quote_ttl_seconds
		FROM goods_products p
		JOIN goods_pricing r ON r.product_id = p.id
		JOIN goods_providers g ON g.slug = p.provider_slug AND g.enabled
		LEFT JOIN goods_product_localizations l ON l.product_id = p.id AND l.locale = $1
		WHERE p.visible AND p.archived_at IS NULL
		ORDER BY p.kind, p.sort_order, p.code`, string(locale))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	products := make([]ShopProduct, 0, 16)
	for rows.Next() {
		var (
			product     ShopProduct
			duration    int32
			quantity    int32
			fixedMinor  *int64
			quoteTTLSec int32
		)
		if err := rows.Scan(&product.ID, &product.Code, &product.ProviderSlug, &product.Kind,
			&duration, &quantity, &product.Name, &product.Description,
			&product.Currency, &product.MarkupBPS, &product.Rounding,
			&fixedMinor, &quoteTTLSec); err != nil {
			return nil, err
		}
		product.DurationMonths, product.StarQuantity = int(duration), int(quantity)
		product.FixedMinor = fixedMinor
		product.QuoteTTL = time.Duration(quoteTTLSec) * time.Second
		if fixedMinor != nil {
			product.PriceMinor, product.PriceKnown = *fixedMinor, true
		}
		products = append(products, product)
	}
	return products, rows.Err()
}

// ShopProduct reads one visible product.
func (store *PostgresStore) ShopProduct(
	ctx context.Context, locale Locale, productID string,
) (ShopProduct, bool, error) {
	products, err := store.ShopProducts(ctx, locale)
	if err != nil {
		return ShopProduct{}, false, err
	}
	for _, product := range products {
		if product.ID == productID {
			return product, true, nil
		}
	}
	return ShopProduct{}, false, nil
}

// PricingRule converts a catalog entry into the domain pricing rule.
func (product ShopProduct) PricingRule() goods.PricingRule {
	return goods.PricingRule{
		Currency: product.Currency, MarkupBPS: product.MarkupBPS,
		Rounding: product.Rounding, FixedAmountMinor: product.FixedMinor,
		QuoteTTL: product.QuoteTTL,
	}
}

// ShopOrder is one of the customer's shop purchases.
//
// `DeliveryStatus` is the honest state of the goods rather than of the payment:
// a paid order whose delivery is still in flight reads as delivering, and one
// an operator is reviewing reads as under review, because "paid" would tell the
// customer nothing about whether their Stars arrived.
type ShopOrder struct {
	OrderID         string
	ProductName     string
	Kind            string
	Quantity        int
	Recipient       string
	RecipientIsSelf bool
	PriceMinor      int64
	Currency        string
	Status          string
	DeliveryStatus  string
	CreatedAt       time.Time
}

// ShopOrders lists the customer's shop history, newest first.
func (store *PostgresStore) ShopOrders(
	ctx context.Context, customerID string, locale Locale, limit int,
) ([]ShopOrder, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT g.order_id::text, COALESCE(l.name, p.code), p.kind, g.quantity,
		       g.recipient_username, g.recipient_is_self, g.quoted_price_minor, g.currency,
		       g.status, COALESCE(d.status, ''), g.created_at
		FROM goods_orders g
		JOIN goods_products p ON p.id = g.product_id
		LEFT JOIN goods_product_localizations l ON l.product_id = p.id AND l.locale = $2
		LEFT JOIN goods_deliveries d ON d.order_id = g.order_id
		WHERE g.user_id = $1::uuid
		ORDER BY g.created_at DESC
		LIMIT $3`, customerID, string(locale), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orders := make([]ShopOrder, 0, limit)
	for rows.Next() {
		var order ShopOrder
		var quantity int32
		if err := rows.Scan(&order.OrderID, &order.ProductName, &order.Kind, &quantity,
			&order.Recipient, &order.RecipientIsSelf, &order.PriceMinor, &order.Currency,
			&order.Status, &order.DeliveryStatus, &order.CreatedAt); err != nil {
			return nil, err
		}
		order.Quantity = int(quantity)
		orders = append(orders, order)
	}
	return orders, rows.Err()
}

// ShopOrderFor reads one of the customer's shop orders.
//
// The customer identifier is part of the predicate rather than checked
// afterwards: the order identifier arrives in callback data, which the customer
// controls, and a query that can only return their own rows cannot be tricked
// into returning somebody else's.
func (store *PostgresStore) ShopOrderFor(
	ctx context.Context, customerID, orderID string, locale Locale,
) (ShopOrder, bool, error) {
	var order ShopOrder
	var quantity int32
	err := store.pool.QueryRow(ctx, `
		SELECT g.order_id::text, COALESCE(l.name, p.code), p.kind, g.quantity,
		       g.recipient_username, g.recipient_is_self, g.quoted_price_minor, g.currency,
		       g.status, COALESCE(d.status, ''), g.created_at
		FROM goods_orders g
		JOIN goods_products p ON p.id = g.product_id
		LEFT JOIN goods_product_localizations l ON l.product_id = p.id AND l.locale = $3
		LEFT JOIN goods_deliveries d ON d.order_id = g.order_id
		WHERE g.user_id = $1::uuid AND g.order_id = $2::uuid`,
		customerID, orderID, string(locale)).
		Scan(&order.OrderID, &order.ProductName, &order.Kind, &quantity,
			&order.Recipient, &order.RecipientIsSelf, &order.PriceMinor, &order.Currency,
			&order.Status, &order.DeliveryStatus, &order.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShopOrder{}, false, nil
	}
	if err != nil {
		return ShopOrder{}, false, err
	}
	order.Quantity = int(quantity)
	return order, true, nil
}

// SentGift is one gift the customer bought, as the sender sees it.
//
// The code hint is the last four characters. It lets a sender tell two of their
// own gifts apart and is not enough for anybody to redeem one; the code itself
// was never stored.
type SentGift struct {
	ID        string
	Kind      string
	Status    string
	CodeHint  string
	Currency  string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// GiftByOrder reads the gift a customer's order bought, so the order screen can
// say what became of it. Ownership is part of the predicate: the order
// identifier arrives in callback data.
func (store *PostgresStore) GiftByOrder(ctx context.Context, customerID, orderID string) (SentGift, bool, error) {
	var gift SentGift
	err := store.pool.QueryRow(ctx,
		`SELECT id::text, kind, status, code_hint, currency, expires_at, created_at
		 FROM gifts WHERE sender_user_id = $1::uuid AND order_id = $2::uuid`, customerID, orderID).
		Scan(&gift.ID, &gift.Kind, &gift.Status, &gift.CodeHint, &gift.Currency, &gift.ExpiresAt, &gift.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SentGift{}, false, nil
	}
	if err != nil {
		return SentGift{}, false, err
	}
	return gift, true, nil
}

// GiftsSent lists the customer's own gifts, newest first.
func (store *PostgresStore) GiftsSent(
	ctx context.Context, customerID string, limit int,
) ([]SentGift, error) {
	rows, err := store.pool.Query(ctx,
		`SELECT id::text, kind, status, code_hint, currency, expires_at, created_at
		 FROM gifts WHERE sender_user_id = $1::uuid
		 ORDER BY created_at DESC LIMIT $2`, customerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sent := make([]SentGift, 0, limit)
	for rows.Next() {
		var gift SentGift
		if err := rows.Scan(&gift.ID, &gift.Kind, &gift.Status, &gift.CodeHint,
			&gift.Currency, &gift.ExpiresAt, &gift.CreatedAt); err != nil {
			return nil, err
		}
		sent = append(sent, gift)
	}
	return sent, rows.Err()
}
