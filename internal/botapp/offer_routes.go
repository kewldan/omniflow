package botapp

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// CustomerOffer is one targeted offer as the customer sees it.
//
// The copy is the operator's, in the customer's language, and the promo code is
// the one the checkout already understands: an offer is a promotion pointed at
// one person, so redeeming it is applying that promotion rather than a second
// discount mechanism with its own rules.
type CustomerOffer struct {
	ID        string
	Title     string
	Terms     string
	PromoCode string
	ExpiresAt time.Time
}

// ActiveOffers reads the offers a customer may still take.
//
// The window is enforced in the query rather than trusted from the sweeper, so
// an offer never outlives its expiry on screen even if the sweeper is behind.
func (store *PostgresStore) ActiveOffers(
	ctx context.Context, customerID string, locale Locale, limit int,
) ([]CustomerOffer, error) {
	titleColumn, termsColumn := "o.title_en", "o.terms_en"
	if locale == LocaleRussian {
		titleColumn, termsColumn = "o.title_ru", "o.terms_ru"
	}
	rows, err := store.pool.Query(ctx, `
		SELECT o.id::text, `+titleColumn+`, COALESCE(`+termsColumn+`, ''),
		       COALESCE(c.normalized_code, ''), o.expires_at
		FROM personal_offers o
		JOIN promotions p ON p.id = o.promotion_id
		LEFT JOIN LATERAL (
			SELECT normalized_code FROM promo_codes
			WHERE promotion_id = p.id AND active
			ORDER BY created_at LIMIT 1
		) c ON true
		WHERE o.user_id = $1::uuid AND o.status = 'active'
		  AND o.starts_at <= now() AND o.expires_at > now()
		ORDER BY o.expires_at
		LIMIT $2`, customerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	offers := make([]CustomerOffer, 0, limit)
	for rows.Next() {
		var offer CustomerOffer
		if err := rows.Scan(&offer.ID, &offer.Title, &offer.Terms,
			&offer.PromoCode, &offer.ExpiresAt); err != nil {
			return nil, err
		}
		offers = append(offers, offer)
	}
	return offers, rows.Err()
}

// DismissOffer records that the customer does not want it.
//
// The customer identifier is part of the predicate, so an offer identifier from
// callback data — which the customer controls — cannot dismiss somebody else's
// offer. Dismissal is final: an offer the customer said no to should not come
// back on the next screen refresh.
func (store *PostgresStore) DismissOffer(ctx context.Context, customerID, offerID string) error {
	tag, err := store.pool.Exec(ctx,
		`UPDATE personal_offers SET status = 'dismissed', resolved_at = now()
		 WHERE id = $1::uuid AND user_id = $2::uuid AND status = 'active'`, offerID, customerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// handleOfferAction serves the offer callback actions.
// offerActions is the closed set of callback actions the offer surface owns.
// It is folded into commerceActions, which is what lets a tap reach the
// handler below at all.
var offerActions = map[string]bool{"offer-take": true, "offer-dismiss": true}

func (app *App) handleOfferAction(
	ctx context.Context, session commerceContext, parts []string,
) (View, bool) {
	argument := ""
	if len(parts) > 1 {
		argument = parts[1]
	}
	switch parts[0] {
	case "offer-dismiss":
		if err := app.customers.DismissOffer(ctx, session.Customer.ID, argument); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			app.logger.Error("offer dismissal failed", "error", err)
		}
		return app.offersScreen(ctx, session), true
	case "offer-take":
		return app.takeOffer(ctx, session, argument), true
	default:
		return View{}, false
	}
}

func (app *App) offersScreen(ctx context.Context, session commerceContext) View {
	offers, err := app.customers.ActiveOffers(ctx, session.Customer.ID, session.Locale, 5)
	if err != nil {
		app.logger.Error("offer lookup failed", "error", err)
		return app.errorView(session.Locale, routeHome)
	}
	return offersView(session.Locale, offers, time.Now().UTC())
}

// takeOffer opens the plan catalog with the offer's promo code applied.
//
// The offer is not marked redeemed here. It is redeemed when an order actually
// uses it, because a customer who opens the catalog and changes their mind has
// not used anything — and burning a single-use offer on a tap would be taking
// something from them for nothing.
func (app *App) takeOffer(ctx context.Context, session commerceContext, offerID string) View {
	offers, err := app.customers.ActiveOffers(ctx, session.Customer.ID, session.Locale, 5)
	if err != nil {
		app.logger.Error("offer lookup failed", "error", err)
		return app.errorView(session.Locale, routeHome)
	}
	for _, offer := range offers {
		if offer.ID != offerID {
			continue
		}
		return offerDetailView(session.Locale, offer, time.Now().UTC())
	}
	return app.offersScreen(ctx, session)
}
