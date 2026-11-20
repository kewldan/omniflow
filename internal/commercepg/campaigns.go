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

// Campaign bonus kinds. Wallet credit and days on the trial plan were the
// first two; trial claims the customer's one trial and plan grants days of a
// plan the operator picked.
const (
	CampaignBonusNone   = "none"
	CampaignBonusWallet = "wallet"
	CampaignBonusDays   = "days"
	CampaignBonusTrial  = "trial"
	CampaignBonusPlan   = "plan"
)

// ErrCampaignUnknown reports a code that names no active campaign.
var ErrCampaignUnknown = errors.New("no active campaign has that code")

// AttributeCampaign records that a /start campaign link brought this customer
// and grants the campaign's sign-up bonus, once. Only a customer created within
// the last hour with no earlier attribution and no paid order is attributed:
// the campaign gets credit for registrations it produced, not for customers it
// merely reached. The bonus is wallet credit, days on the trial plan, the
// trial itself, or days of a plan the operator chose.
//
// The attribution and the bonus commit together, and the bonus is keyed on
// the campaign and the customer, so neither a retried /start nor a second
// campaign can pay a customer twice: customer_campaign_attributions holds one
// row per customer. A subscription bonus that does not apply — a trial the
// customer already had, a plan with no live version — is rolled back to a
// savepoint, and the attribution stands without it.
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
		campaignID  pgtype.UUID
		bonusPlanID pgtype.UUID
		result      CampaignAttribution
	)
	err = tx.QueryRow(ctx, `SELECT id, bonus_kind, bonus_value, currency, bonus_plan_id
		FROM ad_campaigns WHERE code = $1 AND active`, code).
		Scan(&campaignID, &result.BonusKind, &result.BonusValue, &result.Currency, &bonusPlanID)
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
	case CampaignBonusWallet:
		if err = store.creditRewardWallet(ctx, queries, userID, result.Currency, result.BonusValue, key); err != nil {
			return CampaignAttribution{}, err
		}
	case CampaignBonusDays, CampaignBonusTrial, CampaignBonusPlan:
		endsAt, granted, grantErr := store.grantCampaignDays(ctx, tx, userID, result.BonusKind, bonusPlanID,
			result.BonusValue, key)
		if grantErr != nil {
			return CampaignAttribution{}, grantErr
		}
		if !granted {
			result.BonusKind = CampaignBonusNone
			break
		}
		result.BonusEndsAt = endsAt
	}
	if result.BonusKind != CampaignBonusNone {
		if _, err = tx.Exec(ctx, `UPDATE customer_campaign_attributions SET bonus_granted_at = now() WHERE user_id = $1`, userID); err != nil {
			return CampaignAttribution{}, err
		}
	}
	return result, tx.Commit(ctx)
}

// grantCampaignDays grants a campaign's subscription bonus inside a savepoint.
// It reports false, with nothing written, when the bonus does not apply to
// this customer: no live trial plan, a trial already claimed, or a plan with
// no live version.
func (store *Store) grantCampaignDays(
	ctx context.Context, tx pgx.Tx, userID pgtype.UUID, kind string, planID pgtype.UUID, days int64, key string,
) (time.Time, bool, error) {
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return time.Time{}, false, err
	}
	defer func() { _ = savepoint.Rollback(ctx) }()
	queries := dbgen.New(savepoint)
	var (
		planVersionID pgtype.UUID
		currency      string
	)
	switch kind {
	case CampaignBonusPlan:
		version, versionErr := queries.LatestLivePlanVersion(ctx, planID)
		if errors.Is(versionErr, pgx.ErrNoRows) {
			return time.Time{}, false, nil
		}
		if versionErr != nil {
			return time.Time{}, false, versionErr
		}
		planVersionID, currency = version.ID, version.Currency
	default:
		// Days and the trial are both granted on the live trial plan.
		planVersionID, currency, err = store.rewardPlan(ctx, queries, userID, RewardTrial)
		if errors.Is(err, ErrPromoNotApplicable) {
			return time.Time{}, false, nil
		}
		if err != nil {
			return time.Time{}, false, err
		}
	}
	endsAt, _, err := store.grantRewardDays(ctx, savepoint, queries, userID, planVersionID, currency,
		time.Duration(days)*24*time.Hour, kind == CampaignBonusTrial, key)
	if errors.Is(err, ErrPromoNotApplicable) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return endsAt, true, savepoint.Commit(ctx)
}
