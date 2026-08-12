package accountshop

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/goods"
)

// Product is one catalogue entry as the customer sees it.
//
// `PriceMinor` is only meaningful once a price exists without asking anybody:
// an operator who published a fixed price has one immediately, while a product
// priced off a live provider rate has none until the gateway is asked.
// `PriceKnown` says which, so the catalogue can stay silent about a number it
// does not have rather than showing a fabricated one. The detail screen quotes,
// which is where a price acquires its expiry.
//
// The pricing rule travels with the product because the quote is derived from
// it, but nothing here is customer-facing: markup and cost are the operator's
// margin, and the transport layer publishes neither.
type Product struct {
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
	// Available reports whether this product could actually be delivered right
	// now: its gateway resolves and sells this kind of goods. A product whose
	// provider is disabled or uncredentialed is shown as unavailable rather than
	// hidden, so a customer who bookmarked it learns why it will not sell.
	Available    bool
	ProviderSlug string

	quoteTTL   time.Duration
	markupBPS  int
	rounding   string
	fixedMinor *int64
}

// pricingRule converts a catalogue entry into the domain pricing rule. Markup,
// rounding, and the fixed-price opt-out are decided in internal/goods; this only
// carries the operator's configuration to it.
func (product Product) pricingRule() goods.PricingRule {
	return goods.PricingRule{
		Currency: product.Currency, MarkupBPS: product.markupBPS,
		Rounding: product.rounding, FixedAmountMinor: product.fixedMinor,
		QuoteTTL: product.quoteTTL,
	}
}

// catalogQuery is the shape of a visible product, in the customer's language.
//
// It joins the enabled provider rather than filtering afterwards, so a product
// whose gateway an operator switched off leaves the catalogue with the switch
// rather than at the next deployment.
const catalogQuery = `
	SELECT p.id::text, p.code, p.provider_slug, p.kind,
	       COALESCE(p.duration_months, 0), COALESCE(p.star_quantity, 0),
	       COALESCE(l.name, p.code), COALESCE(l.description, ''),
	       r.currency, r.markup_bps, r.rounding, r.fixed_amount_minor, r.quote_ttl_seconds
	FROM goods_products p
	JOIN goods_pricing r ON r.product_id = p.id
	JOIN goods_providers g ON g.slug = p.provider_slug AND g.enabled
	LEFT JOIN goods_product_localizations l ON l.product_id = p.id AND l.locale = $1
	WHERE p.visible AND p.archived_at IS NULL`

// Products lists the catalogue in the customer's language.
//
// A product with no localization for the requested locale falls back to its
// code rather than disappearing: an operator who added a product and has not
// translated it yet should see it and notice, not have it silently vanish for
// half their customers.
//
// No provider is asked for a price here. The catalogue is a list of what is
// sold, and quoting every row would mean one network call per product on a
// screen that is not a purchase — and a set of numbers that would each need
// their own expiry before anybody had chosen anything.
func (service *Service) Products(ctx context.Context, locale string) ([]Product, error) {
	if !service.Enabled() {
		return nil, ErrUnavailable
	}
	rows, err := service.pool.Query(ctx,
		catalogQuery+" ORDER BY p.kind, p.sort_order, p.code", normalizeLocale(locale))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	products := make([]Product, 0, 16)
	for rows.Next() {
		product, scanErr := scanProduct(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		products = append(products, product)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(products) == 0 {
		// An installation with a provider registry wired in but nothing to sell
		// through it is not a shop with an empty shelf. "Sold out" is a state a
		// customer comes back for; "not offered here" is not, and answering with
		// an empty list would make the panel render the first when it means the
		// second. The registry is attached unconditionally by the API process,
		// so this — not the constructor — is where that distinction can be made.
		return nil, ErrUnavailable
	}
	service.markAvailability(ctx, products)
	return products, nil
}

// product reads one visible product.
//
// A product that is archived, hidden, or served by a provider an operator
// disabled is reported as not found, which is also the answer for an identifier
// that never existed. The two are the same answer on purpose: the identifier
// comes out of a URL the customer controls.
func (service *Service) product(ctx context.Context, productID, locale string) (Product, error) {
	if !service.Enabled() {
		return Product{}, ErrUnavailable
	}
	if strings.TrimSpace(productID) == "" {
		return Product{}, accountpg.ErrNotFound
	}
	rows, err := service.pool.Query(ctx,
		catalogQuery+" AND p.id = $2::uuid", normalizeLocale(locale), strings.TrimSpace(productID))
	if err != nil {
		// A malformed identifier fails the uuid cast rather than matching
		// nothing, and a customer pasting a broken URL is a "not found", not a
		// server fault.
		return Product{}, accountpg.ErrNotFound
	}
	defer rows.Close()
	if !rows.Next() {
		return Product{}, accountpg.ErrNotFound
	}
	product, err := scanProduct(rows)
	if err != nil {
		return Product{}, err
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return Product{}, err
	}
	return service.availability(ctx, product), nil
}

func scanProduct(rows pgx.Rows) (Product, error) {
	var (
		product     Product
		duration    int32
		quantity    int32
		fixedMinor  *int64
		quoteTTLSec int32
	)
	if err := rows.Scan(&product.ID, &product.Code, &product.ProviderSlug, &product.Kind,
		&duration, &quantity, &product.Name, &product.Description,
		&product.Currency, &product.markupBPS, &product.rounding,
		&fixedMinor, &quoteTTLSec); err != nil {
		return Product{}, err
	}
	product.DurationMonths, product.StarQuantity = int(duration), int(quantity)
	product.fixedMinor = fixedMinor
	product.quoteTTL = time.Duration(quoteTTLSec) * time.Second
	if fixedMinor != nil {
		product.PriceMinor, product.PriceKnown = *fixedMinor, true
	}
	return product, nil
}

// markAvailability resolves each distinct gateway once and records whether it
// can sell what the product is.
//
// Resolving per slug rather than per product matters because the registry
// decrypts a credential on every call: a catalogue of twenty Stars packs served
// by one gateway costs one lookup, not twenty.
func (service *Service) markAvailability(ctx context.Context, products []Product) {
	resolved := make(map[string]goods.Provider, 2)
	for index, product := range products {
		adapter, seen := resolved[product.ProviderSlug]
		if !seen {
			adapter, _ = service.provider(ctx, product.ProviderSlug)
			resolved[product.ProviderSlug] = adapter
		}
		products[index].Available = adapter != nil && adapter.Supports(product.Kind)
	}
}

// availability is the single-product form, returning the product rather than
// mutating a slice in place.
//
// A gateway that will not resolve makes the product unavailable rather than the
// request a failure. The customer can still see what they were looking at, and
// the purchase refuses on its own terms.
func (service *Service) availability(ctx context.Context, product Product) Product {
	adapter, err := service.provider(ctx, product.ProviderSlug)
	product.Available = err == nil && adapter.Supports(product.Kind)
	return product
}

// normalizeLocale keeps the catalogue join to the two locales the schema
// permits, so an unexpected Accept-Language never turns every product name into
// its code.
func normalizeLocale(locale string) string {
	switch strings.ToLower(strings.TrimSpace(locale)) {
	case "ru":
		return "ru"
	default:
		return "en"
	}
}
