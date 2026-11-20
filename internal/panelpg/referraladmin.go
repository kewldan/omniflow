package panelpg

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// TopReferrer is one inviter on the leaderboard.
type TopReferrer struct {
	CustomerID  string `json:"customerId"`
	Invited     int64  `json:"invited"`
	Qualified   int64  `json:"qualified"`
	EarnedMinor int64  `json:"earnedMinor"`
}

// TopReferrers ranks inviters by what their referrals earned them in the last
// days, then by qualified referrals — Bedolaga's top referrers.
func (service *Service) TopReferrers(ctx context.Context, days int, limit int32) ([]TopReferrer, error) {
	if days <= 0 || days > 3650 {
		days = 30
	}
	rows, err := service.pool.Query(ctx, `SELECT a.referrer_user_id::text,
			count(*) FILTER (WHERE a.created_at > now() - make_interval(days => $1)),
			count(*) FILTER (WHERE a.qualified_at > now() - make_interval(days => $1) AND a.review_state <> 'rejected'),
			COALESCE((SELECT sum(w.amount_minor) FROM referral_rewards w
				WHERE w.beneficiary_user_id = a.referrer_user_id AND w.reversed_at IS NULL
				  AND w.role IN ('inviter', 'commission') AND w.granted_at > now() - make_interval(days => $1)), 0)::bigint
		FROM referral_attributions a
		GROUP BY a.referrer_user_id
		HAVING count(*) FILTER (WHERE a.created_at > now() - make_interval(days => $1)) > 0
		    OR count(*) FILTER (WHERE a.qualified_at > now() - make_interval(days => $1)) > 0
		ORDER BY 4 DESC, 3 DESC, 2 DESC
		LIMIT $2`, days, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	top := []TopReferrer{}
	for rows.Next() {
		var entry TopReferrer
		if err := rows.Scan(&entry.CustomerID, &entry.Invited, &entry.Qualified, &entry.EarnedMinor); err != nil {
			return nil, err
		}
		top = append(top, entry)
	}
	return top, rows.Err()
}

// LostReferral is an invited customer who paid without their inviter being
// rewarded.
type LostReferral struct {
	ReferredID   string    `json:"referredCustomerId"`
	ReferrerID   string    `json:"referrerCustomerId"`
	AttributedAt time.Time `json:"attributedAt"`
	FirstPaidAt  time.Time `json:"firstPaidAt"`
	// Reason is the likely cause: attributed_after_payment, outside_window,
	// or not_rewarded (the programme was off or rewarded nothing then).
	Reason string `json:"reason"`
}

// LostReferrals is Bedolaga's referral diagnostics: pairs whose friend has a
// settled subscription order but which never qualified.
func (service *Service) LostReferrals(ctx context.Context, limit int32) ([]LostReferral, error) {
	rows, err := service.pool.Query(ctx, `SELECT a.referred_user_id::text, a.referrer_user_id::text, a.created_at,
			first.paid_at,
			CASE WHEN first.created_at < a.created_at THEN 'attributed_after_payment'
			     WHEN first.created_at > a.created_at + make_interval(days => COALESCE(p.attribution_validity_days, 90)) THEN 'outside_window'
			     ELSE 'not_rewarded' END
		FROM referral_attributions a
		LEFT JOIN referral_programs p ON p.singleton
		JOIN LATERAL (
			SELECT o.created_at, COALESCE(o.paid_at, o.created_at) AS paid_at FROM orders o
			WHERE o.user_id = a.referred_user_id
			  AND o.state IN ('paid', 'fulfilled', 'partially_refunded', 'refunded')
			  AND o.operation IN ('purchase', 'extension', 'renewal', 'upgrade', 'downgrade')
			ORDER BY o.created_at LIMIT 1
		) first ON true
		WHERE a.qualified_at IS NULL AND a.review_state <> 'rejected'
		ORDER BY first.paid_at DESC
		LIMIT $1`, pageSize(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lost := []LostReferral{}
	for rows.Next() {
		var entry LostReferral
		if err := rows.Scan(&entry.ReferredID, &entry.ReferrerID, &entry.AttributedAt, &entry.FirstPaidAt, &entry.Reason); err != nil {
			return nil, err
		}
		lost = append(lost, entry)
	}
	return lost, rows.Err()
}

// ErrReferralLocked refuses to detach a pair that already paid out: the
// rewards are reversed by rejecting it in review, which keeps the record.
var ErrReferralLocked = errors.New("referral has live rewards")

// AttachReferrer records who invited a customer, by the inviter's code, the way
// an operator fixes a referral the link lost. The usual rules hold: no
// self-referral and no second inviter.
func (service *Service) AttachReferrer(ctx context.Context, customerID, code string, actor Actor) error {
	id, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	return service.inTxRaw(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		var referrer pgtype.UUID
		err := tx.QueryRow(ctx, `SELECT user_id FROM referral_codes WHERE code = $1`, code).Scan(&referrer)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && referrer == id {
			return ErrValidaton
		}
		if err != nil {
			return err
		}
		inserted, err := tx.Exec(ctx, `INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code)
			VALUES ($1, $2, $3) ON CONFLICT (referred_user_id) DO NOTHING`, id, referrer, code)
		if err != nil {
			return err
		}
		if inserted.RowsAffected() == 0 {
			return ErrConflict
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.referral.attached", "marketing", "customer", customerID,
			map[string]any{"referrerId": uuidString(referrer), "code": code},
		))
	})
}

// DetachReferrer removes an attribution that has paid nothing out.
func (service *Service) DetachReferrer(ctx context.Context, customerID string, actor Actor) error {
	id, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	return service.inTxRaw(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		var live int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM referral_rewards
			WHERE referred_user_id = $1 AND reversed_at IS NULL`, id).Scan(&live); err != nil {
			return err
		}
		if live > 0 {
			return ErrReferralLocked
		}
		removed, err := tx.Exec(ctx, `DELETE FROM referral_attributions WHERE referred_user_id = $1`, id)
		if err != nil {
			return err
		}
		if removed.RowsAffected() == 0 {
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.referral.detached", "marketing", "customer", customerID, nil,
		))
	})
}

// InviterTerms are an operator's personal commission rates for one inviter.
type InviterTerms struct {
	CommissionBPS             *int32 `json:"commissionBps,omitempty"`
	FirstPaymentCommissionBPS *int32 `json:"firstPaymentCommissionBps,omitempty"`
	Note                      string `json:"note,omitempty"`
}

// ReferralInviterTerms reads a customer's personal rates.
func (service *Service) ReferralInviterTerms(ctx context.Context, customerID string) (InviterTerms, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return InviterTerms{}, err
	}
	var terms InviterTerms
	var rate, first pgtype.Int4
	err = service.pool.QueryRow(ctx, `SELECT commission_bps, first_payment_commission_bps, note
		FROM referral_inviter_terms WHERE user_id = $1`, id).Scan(&rate, &first, &terms.Note)
	if errors.Is(err, pgx.ErrNoRows) {
		return InviterTerms{}, nil
	}
	terms.CommissionBPS, terms.FirstPaymentCommissionBPS = int32Pointer(rate), int32Pointer(first)
	return terms, err
}

// SaveReferralInviterTerms sets or, with both rates empty, removes a
// customer's personal rates.
func (service *Service) SaveReferralInviterTerms(ctx context.Context, customerID string, terms InviterTerms, actor Actor) error {
	id, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	for _, bps := range []*int32{terms.CommissionBPS, terms.FirstPaymentCommissionBPS} {
		if bps != nil && (*bps < 0 || *bps > 10000) {
			return ErrValidaton
		}
	}
	if len([]rune(terms.Note)) > 400 {
		return ErrValidaton
	}
	return service.inTxRaw(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		if terms.CommissionBPS == nil && terms.FirstPaymentCommissionBPS == nil {
			if _, err := tx.Exec(ctx, `DELETE FROM referral_inviter_terms WHERE user_id = $1`, id); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `INSERT INTO referral_inviter_terms
				(user_id, commission_bps, first_payment_commission_bps, note, updated_by)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (user_id) DO UPDATE SET commission_bps = EXCLUDED.commission_bps,
				first_payment_commission_bps = EXCLUDED.first_payment_commission_bps,
				note = EXCLUDED.note, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			id, optionalInt4(terms.CommissionBPS), optionalInt4(terms.FirstPaymentCommissionBPS),
			terms.Note, optionalUUID(actor.AdminID)); err != nil {
			return err
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.referral.terms_saved", "marketing", "customer", customerID,
			map[string]any{"commissionBps": terms.CommissionBPS, "firstPaymentBps": terms.FirstPaymentCommissionBPS},
		))
	})
}
