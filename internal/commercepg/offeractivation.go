package commercepg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// ErrOfferUnavailable reports an offer that is not the customer's, or not live.
var ErrOfferUnavailable = errors.New("offer is not available")

// OfferActivation is what activating an offer did.
type OfferActivation struct {
	// Activated is false when the offer had been activated before.
	Activated  bool
	ExpiresAt  time.Time
	BonusMinor int64
	Currency   string
}

// ActivatePersonalOffer starts an offer's discount timer and pays its balance
// bonus, once — Bedolaga's promo offers that run for a number of hours from
// the moment the customer takes them. An offer without a timer keeps its end;
// a second activation changes nothing.
func (store *Store) ActivatePersonalOffer(ctx context.Context, customerID, offerID string) (OfferActivation, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return OfferActivation{}, err
	}
	id, err := parseUUID(offerID)
	if err != nil {
		return OfferActivation{}, ErrOfferUnavailable
	}
	var result OfferActivation
	err = retrySerializable(ctx, func() error {
		tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		var (
			hours     pgtype.Int4
			activated pgtype.Timestamptz
			currency  pgtype.Text
		)
		err = tx.QueryRow(ctx, `SELECT activation_hours, activated_at, expires_at, bonus_minor, bonus_currency
			FROM personal_offers WHERE id = $1 AND user_id = $2 AND status = 'active' AND expires_at > now()
			FOR UPDATE`, id, userID).Scan(&hours, &activated, &result.ExpiresAt, &result.BonusMinor, &currency)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOfferUnavailable
		}
		if err != nil {
			return err
		}
		result.Currency = currency.String
		if activated.Valid {
			result.Activated = false
			return tx.Commit(ctx)
		}
		result.Activated = true
		if hours.Valid {
			result.ExpiresAt = store.clock().UTC().Add(time.Duration(hours.Int32) * time.Hour)
		}
		if _, err = tx.Exec(ctx, `UPDATE personal_offers SET activated_at = now(), expires_at = $2 WHERE id = $1`,
			id, result.ExpiresAt); err != nil {
			return err
		}
		if result.BonusMinor > 0 {
			if err = store.creditRewardWallet(ctx, dbgen.New(tx), userID, result.Currency, result.BonusMinor,
				"offer-bonus:"+offerID); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	})
	return result, err
}
