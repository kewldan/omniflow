package commercepg

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// campaignCodePattern matches ad_campaigns.code.
var campaignCodePattern = regexp.MustCompile(`^[a-z0-9_]{2,40}$`)

// campaignAttributionWindow is how new a customer must be for a /start
// campaign link to claim them. An existing customer tapping an ad is not a
// registration the campaign produced.
const campaignAttributionWindow = time.Hour

// CampaignAttribution is what attributing a customer to a campaign did.
type CampaignAttribution struct {
	Attributed  bool
	BonusKind   string
	BonusValue  int64
	Currency    string
	BonusEndsAt time.Time
}

// ErrCampaignUnknown reports a code that names no active campaign.
var ErrCampaignUnknown = errors.New("no active campaign has that code")

// AttributeCampaign records that a /start campaign link brought this customer
// and grants the campaign's sign-up bonus, once. Only a customer created within
// the last hour with no earlier attribution and no paid order is attributed:
// the campaign gets credit for registrations it produced, not for customers it
// merely reached. The bonus is wallet credit or days on the trial plan.
func (store *Store) AttributeCampaign(ctx context.Context, customerID, code string) (CampaignAttribution, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if !campaignCodePattern.MatchString(code) {
		return CampaignAttribution{}, ErrCampaignUnknown
	}
	userID, err := parseUUID(customerID)
	if err != nil {
		return CampaignAttribution{}, err
	}
	var result CampaignAttribution
	err = retrySerializable(ctx, func() error {
		var attemptErr error
		result, attemptErr = store.attributeCampaign(ctx, userID, code)
		return attemptErr
	})
	return result, err
}

func (store *Store) attributeCampaign(ctx context.Context, userID pgtype.UUID, code string) (CampaignAttribution, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return CampaignAttribution{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var (
		campaignID pgtype.UUID
		result     CampaignAttribution
	)
	err = tx.QueryRow(ctx, `SELECT id, bonus_kind, bonus_value, currency FROM ad_campaigns WHERE code = $1 AND active`, code).
		Scan(&campaignID, &result.BonusKind, &result.BonusValue, &result.Currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return CampaignAttribution{}, ErrCampaignUnknown
	}
	if err != nil {
		return CampaignAttribution{}, err
	}
	var eligible bool
	if err = tx.QueryRow(ctx, `SELECT u.created_at > now() - $2::interval
		AND NOT EXISTS (SELECT 1 FROM customer_campaign_attributions a WHERE a.user_id = u.id)
		AND NOT EXISTS (SELECT 1 FROM orders o WHERE o.user_id = u.id AND o.state IN ('paid','fulfilled','partially_refunded','refunded'))
		FROM users u WHERE u.id = $1`, userID, campaignAttributionWindow.String()).Scan(&eligible); err != nil || !eligible {
		return CampaignAttribution{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO customer_campaign_attributions (user_id, campaign_id) VALUES ($1, $2)`, userID, campaignID); err != nil {
		return CampaignAttribution{}, err
	}
	result.Attributed = true
	queries := dbgen.New(tx)
	key := "campaign:" + uuidString(campaignID) + ":" + uuidString(userID)
	switch result.BonusKind {
	case "wallet":
		if err = store.creditRewardWallet(ctx, queries, userID, result.Currency, result.BonusValue, key); err != nil {
			return CampaignAttribution{}, err
		}
	case "days":
		planVersionID, currency, planErr := store.rewardPlan(ctx, queries, userID, RewardTrial)
		if errors.Is(planErr, ErrPromoNotApplicable) {
			// No trial plan to grant days on: the attribution stands without a bonus.
			result.BonusKind = "none"
			break
		}
		if planErr != nil {
			return CampaignAttribution{}, planErr
		}
		endsAt, _, grantErr := store.grantRewardDays(ctx, tx, queries, userID, planVersionID, currency,
			time.Duration(result.BonusValue)*24*time.Hour, false, key)
		if grantErr != nil {
			return CampaignAttribution{}, grantErr
		}
		result.BonusEndsAt = endsAt
	}
	if result.BonusKind != "none" {
		if _, err = tx.Exec(ctx, `UPDATE customer_campaign_attributions SET bonus_granted_at = now() WHERE user_id = $1`, userID); err != nil {
			return CampaignAttribution{}, err
		}
	}
	return result, tx.Commit(ctx)
}
