package accountreferral

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrRewardChoiceUnavailable reports a choice the programme does not offer.
var ErrRewardChoiceUnavailable = errors.New("the programme does not offer a choice of reward")

// Invitee is one friend as their inviter sees them: when they joined, whether
// they paid, and what they earned the inviter. Nothing identifies them.
type Invitee struct {
	JoinedAt    time.Time
	Paid        bool
	EarnedMinor int64
}

// ReferralInsights is what the referral screen shows beside the summary.
type ReferralInsights struct {
	// ChoiceAllowed is true when the programme rewards in both days and money
	// and lets the customer pick; Kind is their pick, empty for the default.
	ChoiceAllowed bool
	Kind          string
	// Earnings over the last week, month, and all time.
	Last7Minor, Last30Minor, AllMinor int64
	Invitees                          []Invitee
}

// Insights reads the customer's reward choice, earnings by period, and their
// invitees.
func (service *Service) Insights(ctx context.Context, customerID string) (ReferralInsights, error) {
	var insights ReferralInsights
	err := service.pool.QueryRow(ctx, `SELECT
			COALESCE((SELECT allow_reward_choice
				AND (inviter_reward_minor > 0 OR invitee_reward_minor > 0)
				AND (inviter_reward_days > 0 OR invitee_reward_days > 0)
				FROM referral_programs WHERE singleton), false),
			COALESCE((SELECT kind FROM referral_reward_preferences WHERE user_id = $1::uuid), ''),
			COALESCE(sum(amount_minor) FILTER (WHERE granted_at > now() - interval '7 days'), 0)::bigint,
			COALESCE(sum(amount_minor) FILTER (WHERE granted_at > now() - interval '30 days'), 0)::bigint,
			COALESCE(sum(amount_minor), 0)::bigint
		FROM referral_rewards WHERE beneficiary_user_id = $1::uuid AND reversed_at IS NULL
		  AND role IN ('inviter', 'commission')`, customerID).Scan(
		&insights.ChoiceAllowed, &insights.Kind, &insights.Last7Minor, &insights.Last30Minor, &insights.AllMinor)
	if err != nil {
		return ReferralInsights{}, err
	}
	rows, err := service.pool.Query(ctx, `SELECT a.created_at, a.qualified_at IS NOT NULL,
			COALESCE((SELECT sum(w.amount_minor) FROM referral_rewards w
				WHERE w.referred_user_id = a.referred_user_id AND w.beneficiary_user_id = $1::uuid
				  AND w.reversed_at IS NULL), 0)::bigint
		FROM referral_attributions a WHERE a.referrer_user_id = $1::uuid AND a.review_state <> 'rejected'
		ORDER BY a.created_at DESC LIMIT 100`, customerID)
	if err != nil {
		return ReferralInsights{}, err
	}
	defer rows.Close()
	insights.Invitees = []Invitee{}
	for rows.Next() {
		var invitee Invitee
		if err := rows.Scan(&invitee.JoinedAt, &invitee.Paid, &invitee.EarnedMinor); err != nil {
			return ReferralInsights{}, err
		}
		insights.Invitees = append(insights.Invitees, invitee)
	}
	return insights, rows.Err()
}

// ChooseRewardKind records whether the customer wants referral rewards as
// days or as balance, when the programme offers the choice.
func (service *Service) ChooseRewardKind(ctx context.Context, customerID, kind string) error {
	if kind != "wallet" && kind != "days" {
		return ErrRewardChoiceUnavailable
	}
	var allowed bool
	err := service.pool.QueryRow(ctx, `SELECT allow_reward_choice FROM referral_programs WHERE singleton`).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !allowed {
		return ErrRewardChoiceUnavailable
	}
	if err != nil {
		return err
	}
	_, err = service.pool.Exec(ctx, `INSERT INTO referral_reward_preferences (user_id, kind) VALUES ($1::uuid, $2)
		ON CONFLICT (user_id) DO UPDATE SET kind = EXCLUDED.kind, updated_at = now()`, customerID, kind)
	return err
}
