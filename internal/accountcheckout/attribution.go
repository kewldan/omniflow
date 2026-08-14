package accountcheckout

import (
	"context"
	"errors"

	"github.com/omniflow/omniflow/internal/adtracking"
)

// Recording where an order came from.
//
// The browser captured the advertising parameters on the visit that started
// this purchase and held them first-party. It hands them over once, against one
// order, and only when the visitor has agreed to measurement — the storefront
// never sends this otherwise, and there is no other path into the table.
//
// Deliberately not on the checkout request. A checkout is created from the bot
// as well as from the browser, and the bot has no URL to have carried anything;
// widening the purchase path with a field that is always empty on one of its two
// callers would put an advertising concern into the middle of buying something.
//
// The ownership check is the security of it. Without it a customer could attach
// an advertising origin to somebody else's purchase, which would both corrupt
// the operator's export and confirm that the order exists.

// ErrNotYourOrder is returned when the order is not the caller's, or does not
// exist. The two are deliberately the same answer.
var ErrNotYourOrder = errors.New("order not found")

// RecordAttribution attaches an advertising origin to one of the customer's own
// orders.
func (service *Service) RecordAttribution(
	ctx context.Context, customerID, orderID string, attribution adtracking.Attribution,
) error {
	cleaned := adtracking.Clean(attribution)
	if err := adtracking.CheckAttribution(cleaned); err != nil {
		return err
	}
	owned, err := service.store.orderBelongsTo(ctx, customerID, orderID)
	if err != nil {
		return err
	}
	if !owned {
		return ErrNotYourOrder
	}
	return service.store.recordAttribution(ctx, orderID, cleaned)
}

func (store *Store) orderBelongsTo(ctx context.Context, customerID, orderID string) (bool, error) {
	var owned bool
	err := store.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM orders WHERE id = $1::uuid AND user_id = $2::uuid)`,
		orderID, customerID,
	).Scan(&owned)
	return owned, err
}

// recordAttribution is an upsert. A customer who reloads the checkout with the
// same parameters is recording the same fact, and one who resumed from a
// different advertisement is recording a fact that changed.
func (store *Store) recordAttribution(
	ctx context.Context, orderID string, attribution adtracking.Attribution,
) error {
	_, err := store.pool.Exec(ctx, `
		INSERT INTO order_attributions (
			order_id, click_id, click_source, utm_source, utm_medium, utm_campaign,
			utm_content, utm_term
		) VALUES (
			$1::uuid, NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''),
			NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, '')
		)
		ON CONFLICT (order_id) DO UPDATE
		SET click_id = EXCLUDED.click_id, click_source = EXCLUDED.click_source,
			utm_source = EXCLUDED.utm_source, utm_medium = EXCLUDED.utm_medium,
			utm_campaign = EXCLUDED.utm_campaign, utm_content = EXCLUDED.utm_content,
			utm_term = EXCLUDED.utm_term, recorded_at = now()`,
		orderID, attribution.ClickID, attribution.ClickSource, attribution.Source,
		attribution.Medium, attribution.Campaign, attribution.Content, attribution.Term)
	return err
}
