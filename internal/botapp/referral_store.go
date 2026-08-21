package botapp

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
)

// ReferralSummary is the customer-facing state of a referral program.
type ReferralSummary struct {
	Code           string
	Invited        int64
	Qualified      int64
	RewardedMinor  int64
	RewardCount    int
	Currency       string
	RemainingSlots *int
	TermsURL       string
	Enabled        bool
}

// ReferralProgram reads the operator-configured program, including the terms
// link shown to customers.
func (store *PostgresStore) ReferralProgram(ctx context.Context) (commerce.ReferralProgram, string, error) {
	var (
		program            commerce.ReferralProgram
		rewardCap          pgtype.Int4
		validityDays       int32
		rewardExpiryDays   pgtype.Int4
		termsURL           pgtype.Text
		qualificationValue string
	)
	err := store.pool.QueryRow(ctx, `SELECT enabled, currency, inviter_reward_minor, invitee_reward_minor,
		qualification, inviter_reward_cap, attribution_validity_days, reward_expiry_days, terms_url
		FROM referral_programs WHERE singleton`).Scan(&program.Enabled, &program.Currency,
		&program.InviterRewardMinor, &program.InviteeRewardMinor, &qualificationValue, &rewardCap,
		&validityDays, &rewardExpiryDays, &termsURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return commerce.ReferralProgram{}, "", nil
	}
	if err != nil {
		return commerce.ReferralProgram{}, "", err
	}
	program.Qualification = qualificationValue
	program.AttributionValidity = time.Duration(validityDays) * 24 * time.Hour
	if rewardCap.Valid {
		value := int(rewardCap.Int32)
		program.InviterRewardCap = &value
	}
	if rewardExpiryDays.Valid {
		expiry := time.Duration(rewardExpiryDays.Int32) * 24 * time.Hour
		program.RewardExpiry = &expiry
	}
	return program, termsURL.String, nil
}

// ReferralSummary builds the customer's referral code, progress, and rewards.
func (store *PostgresStore) ReferralSummary(ctx context.Context, customerID string) (ReferralSummary, error) {
	program, termsURL, err := store.ReferralProgram(ctx)
	if err != nil {
		return ReferralSummary{}, err
	}
	code, err := newReferralCode()
	if err != nil {
		return ReferralSummary{}, err
	}
	summary := ReferralSummary{TermsURL: termsURL, Enabled: program.Enabled, Currency: program.Currency}
	err = store.pool.QueryRow(ctx, `WITH mine AS (
			INSERT INTO referral_codes (user_id, code) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id
			RETURNING user_id, code
		)
		SELECT mine.code,
			(SELECT count(*) FROM referral_attributions a WHERE a.referrer_user_id = mine.user_id),
			(SELECT count(*) FROM referral_attributions a WHERE a.referrer_user_id = mine.user_id AND a.qualified_at IS NOT NULL),
			-- Reversed rewards are excluded from both the total and the count.
			-- An operator reverses one when the referral turns out to be abuse,
			-- and it is compensated in the ledger rather than deleted, so the row
			-- stays. Counting it would tell the customer they had earned money
			-- that was taken back, and — because the count feeds the inviter
			-- reward cap through Progress below — it would also keep a slot
			-- consumed by a referral the operator has already rejected. The
			-- reversal columns arrived in v0.8 and this aggregate predates them.
			(SELECT COALESCE(sum(w.amount_minor), 0) FROM referral_rewards w
				WHERE w.beneficiary_user_id = mine.user_id AND w.reversed_at IS NULL),
			(SELECT count(*)::integer FROM referral_rewards w
				WHERE w.beneficiary_user_id = mine.user_id AND w.role = 'inviter'
				  AND w.reversed_at IS NULL)
		FROM mine`, customerID, code).
		Scan(&summary.Code, &summary.Invited, &summary.Qualified, &summary.RewardedMinor, &summary.RewardCount)
	if err != nil {
		return ReferralSummary{}, err
	}
	progress := program.Progress(summary.Invited, summary.Qualified, summary.RewardedMinor, summary.RewardCount)
	summary.RemainingSlots = progress.RemainingSlots
	return summary, nil
}

// ReferralRewardEntry is one granted referral reward the customer can review.
type ReferralRewardEntry struct {
	Role        string
	AmountMinor int64
	Currency    string
	GrantedAt   time.Time
}

// ReferralRewards lists the customer's reward history, newest first.
func (store *PostgresStore) ReferralRewards(ctx context.Context, customerID string, limit int) ([]ReferralRewardEntry, error) {
	rows, err := store.pool.Query(ctx, `SELECT role, amount_minor, currency, granted_at
		FROM referral_rewards WHERE beneficiary_user_id = $1::uuid
		ORDER BY granted_at DESC LIMIT $2`, customerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]ReferralRewardEntry, 0, limit)
	for rows.Next() {
		var entry ReferralRewardEntry
		if err := rows.Scan(&entry.Role, &entry.AmountMinor, &entry.Currency, &entry.GrantedAt); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// AttributeReferralForCustomer records an immutable inviter/invitee pair. The
// primary key on the invited customer makes attribution first-write-wins, and
// self-referral is impossible by construction.
//
// Two further conditions keep the pair honest. The programme has to be on:
// an attribution written while it is off would pay out the day an operator
// turns it on, for an invitation nobody was offering. And the invitee has to be
// new — no paid order on record — because a referral rewards bringing a
// customer in, and a customer who has already paid was not brought in by the
// link they tapped today.
func (store *PostgresStore) AttributeReferralForCustomer(ctx context.Context, customerID, code string) error {
	program, _, err := store.ReferralProgram(ctx)
	if err != nil {
		return err
	}
	if !program.Enabled {
		return nil
	}
	_, err = store.pool.Exec(ctx, `INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code)
		SELECT $1::uuid, owner.user_id, owner.code
		FROM referral_codes owner
		WHERE owner.code = $2 AND owner.user_id <> $1::uuid
		  AND NOT EXISTS (
			SELECT 1 FROM orders o
			WHERE o.user_id = $1::uuid
			  AND o.state IN ('paid', 'fulfilled', 'partially_refunded', 'refunded')
		  )
		ON CONFLICT (referred_user_id) DO NOTHING`, customerID, strings.ToUpper(strings.TrimSpace(code)))
	return err
}
