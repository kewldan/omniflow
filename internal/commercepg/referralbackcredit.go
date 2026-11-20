package commercepg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// ErrReferralNotLost reports a pair that has nothing to back-credit: no
// attribution, one already qualified or rejected, or a friend with no settled
// subscription order.
var ErrReferralNotLost = errors.New("referral is not lost")

// ErrReferralNothingGranted reports a back-credit the programme pays nothing
// for: it is switched off, or rewards nothing for this order.
var ErrReferralNothingGranted = errors.New("the referral programme grants nothing for this order")

// BackCreditReferral grants the rewards a referral missed — Bedolaga's
// back-crediting from referral diagnostics — on the friend's first settled
// subscription order, under the programme in force. The attribution window is
// not applied: an operator crediting a lost referral has decided it counts.
// It is exactly-once like a live grant: rewards are keyed by the pair and
// role, and a qualified pair is not lost any more.
func (store *Store) BackCreditReferral(ctx context.Context, referredID string) error {
	userID, err := parseUUID(referredID)
	if err != nil {
		return err
	}
	return retrySerializable(ctx, func() error {
		tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		var lost bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM referral_attributions
			WHERE referred_user_id = $1 AND qualified_at IS NULL AND review_state <> 'rejected')`, userID).Scan(&lost); err != nil {
			return err
		}
		if !lost {
			return ErrReferralNotLost
		}
		queries := dbgen.New(tx)
		var orderID pgtype.UUID
		err = tx.QueryRow(ctx, `SELECT id FROM orders WHERE user_id = $1
			AND state IN ('paid', 'fulfilled', 'partially_refunded', 'refunded')
			AND operation IN ('purchase', 'extension', 'renewal', 'upgrade', 'downgrade')
			ORDER BY created_at LIMIT 1`, userID).Scan(&orderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrReferralNotLost
		}
		if err != nil {
			return err
		}
		order, err := queries.LockOrder(ctx, orderID)
		if err != nil {
			return err
		}
		program, err := store.referralProgram(ctx, tx)
		if err != nil {
			return err
		}
		program.AttributionValidity = 0
		if err = store.grantReferralRewards(ctx, tx, queries, order, program); err != nil {
			return err
		}
		var qualified bool
		if err = tx.QueryRow(ctx, `SELECT qualified_at IS NOT NULL FROM referral_attributions
			WHERE referred_user_id = $1`, userID).Scan(&qualified); err != nil {
			return err
		}
		if !qualified {
			// The programme is off or rewards nothing for this order.
			return ErrReferralNothingGranted
		}
		return tx.Commit(ctx)
	})
}
