package accountcheckout

import (
	"context"
	"errors"
	"time"

	"github.com/omniflow/omniflow/internal/commercepg"
)

// ErrOfferNotFound is an offer that is not this customer's, or no longer
// active.
var ErrOfferNotFound = errors.New("offer not found")

// Offer is one personal offer as the customer panel shows it.
//
// The copy is the operator's in the customer's language. The checkout applies
// the best active offer by itself, so the panel never asks the customer to
// type the code; it is returned so the screen can name it.
type Offer struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Terms       string     `json:"terms"`
	PromoCode   string     `json:"promoCode,omitempty"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	ActivatedAt *time.Time `json:"activatedAt,omitempty"`
}

// OfferTaken is what taking an offer did.
type OfferTaken struct {
	// Activated is false when the offer had been taken before.
	Activated bool `json:"activated"`
	// ExpiresAt is when the discount ends, which a timed offer moves to
	// "hours from now" on the first take.
	ExpiresAt time.Time `json:"expiresAt"`
	// Bonus is the balance credited on the first take, if the offer has one.
	BonusMinor int64  `json:"bonusMinor"`
	Currency   string `json:"currency,omitempty"`
}

// Offers lists the personal offers the customer may still use, soonest to
// end first. The window is enforced here rather than trusted from the
// sweeper, so an offer never outlives its end on screen.
func (service *Service) Offers(ctx context.Context, customerID, locale string) ([]Offer, error) {
	titleColumn, termsColumn := "o.title_en", "o.terms_en"
	if locale == "ru" {
		titleColumn, termsColumn = "o.title_ru", "o.terms_ru"
	}
	rows, err := service.store.pool.Query(ctx, `
		SELECT o.id::text, `+titleColumn+`, COALESCE(`+termsColumn+`, ''),
		       COALESCE(c.normalized_code, ''), o.expires_at, o.activated_at
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
		LIMIT 20`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	offers := []Offer{}
	for rows.Next() {
		var offer Offer
		if err := rows.Scan(&offer.ID, &offer.Title, &offer.Terms, &offer.PromoCode,
			&offer.ExpiresAt, &offer.ActivatedAt); err != nil {
			return nil, err
		}
		offers = append(offers, offer)
	}
	return offers, rows.Err()
}

// TakeOffer starts the offer's discount timer and pays its balance bonus,
// once, exactly as taking it in the bot does. The offer is redeemed only when
// an order uses it.
func (service *Service) TakeOffer(ctx context.Context, customerID, offerID string) (OfferTaken, error) {
	activation, err := service.orders.ActivatePersonalOffer(ctx, customerID, offerID)
	if errors.Is(err, commercepg.ErrOfferUnavailable) {
		return OfferTaken{}, ErrOfferNotFound
	}
	if err != nil {
		return OfferTaken{}, err
	}
	return OfferTaken{
		Activated: activation.Activated, ExpiresAt: activation.ExpiresAt,
		BonusMinor: activation.BonusMinor, Currency: activation.Currency,
	}, nil
}

// DismissOffer drops an offer the customer does not want — Bedolaga's "reset
// the active discount". The customer is part of the predicate, so an
// identifier from the request cannot dismiss somebody else's offer, and a
// dismissed offer stops being applied at checkout at once.
func (service *Service) DismissOffer(ctx context.Context, customerID, offerID string) error {
	tag, err := service.store.pool.Exec(ctx,
		`UPDATE personal_offers SET status = 'dismissed', resolved_at = now()
		 WHERE id = $1::uuid AND user_id = $2::uuid AND status = 'active'`, offerID, customerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrOfferNotFound
	}
	return nil
}
