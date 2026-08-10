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
			(SELECT COALESCE(sum(w.amount_minor), 0) FROM referral_rewards w WHERE w.beneficiary_user_id = mine.user_id),
			(SELECT count(*)::integer FROM referral_rewards w WHERE w.beneficiary_user_id = mine.user_id AND w.role = 'inviter')
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
func (store *PostgresStore) AttributeReferralForCustomer(ctx context.Context, customerID, code string) error {
	_, err := store.pool.Exec(ctx, `INSERT INTO referral_attributions (referred_user_id, referrer_user_id, code)
		SELECT $1::uuid, owner.user_id, owner.code
		FROM referral_codes owner
		WHERE owner.code = $2 AND owner.user_id <> $1::uuid
		ON CONFLICT (referred_user_id) DO NOTHING`, customerID, strings.ToUpper(strings.TrimSpace(code)))
	return err
}
