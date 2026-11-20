package panelpg

import (
	"context"
	"math"

	"github.com/jackc/pgx/v5"
)

// AdCampaignDay is one UTC day of a campaign's results. Registrations are
// counted on the day of attribution, trials on the day they were claimed,
// FirstPayments on the day of each customer's first settled subscription
// order, and RevenueMinor is what the campaign's customers' orders settled
// that day, net of refunds, in the campaign currency.
type AdCampaignDay struct {
	Day           string `json:"day"`
	Registrations int64  `json:"registrations"`
	Trials        int64  `json:"trials"`
	FirstPayments int64  `json:"firstPayments"`
	RevenueMinor  int64  `json:"revenueMinor"`
}

// AdCampaignStats is a campaign's report: totals over its whole life, the
// conversions they imply, and a series by day for the chart.
type AdCampaignStats struct {
	Campaign AdCampaign `json:"campaign"`
	// TrialPercent is the share of registrations that took a trial;
	// PayingPercent the share that paid; TrialToPaidPercent the share of
	// trial takers who paid. Each is zero when its base is.
	TrialPercent       float64 `json:"trialPercent"`
	PayingPercent      float64 `json:"payingPercent"`
	TrialToPaidPercent float64 `json:"trialToPaidPercent"`
	// RevenuePerRegistrationMinor is the campaign's revenue divided by its
	// registrations, rounded down.
	RevenuePerRegistrationMinor int64           `json:"revenuePerRegistrationMinor"`
	Days                        []AdCampaignDay `json:"days"`
}

// settledSubscription is the order predicate a "paying" customer meets: a
// settled subscription order that took money.
const settledSubscription = `o.state IN ('paid','fulfilled','partially_refunded')
	AND o.operation IN ('purchase','extension','renewal','upgrade','downgrade') AND o.paid_minor > 0`

// AdCampaignStats reads one campaign's report with a series of the last days
// (1–365, default 30), oldest first, with every day present.
func (service *Service) AdCampaignStats(ctx context.Context, campaignID string, days int) (AdCampaignStats, error) {
	id, err := parseUUID(campaignID)
	if err != nil {
		return AdCampaignStats{}, ErrNotFound
	}
	if days <= 0 || days > 365 {
		days = 30
	}
	campaigns, err := service.AdCampaigns(ctx)
	if err != nil {
		return AdCampaignStats{}, err
	}
	stats := AdCampaignStats{Days: []AdCampaignDay{}}
	found := false
	for _, campaign := range campaigns {
		if campaign.ID == uuidString(id) {
			stats.Campaign, found = campaign, true
		}
	}
	if !found {
		return AdCampaignStats{}, ErrNotFound
	}
	stats.TrialPercent = percentOf(stats.Campaign.Trials, stats.Campaign.Registrations)
	stats.PayingPercent = percentOf(stats.Campaign.Paying, stats.Campaign.Registrations)
	if stats.Campaign.Registrations > 0 {
		stats.RevenuePerRegistrationMinor = stats.Campaign.RevenueMinor / stats.Campaign.Registrations
	}
	var trialPayers int64
	if err = service.pool.QueryRow(ctx, `SELECT count(DISTINCT a.user_id) FROM customer_campaign_attributions a
		JOIN trial_claims t ON t.user_id = a.user_id
		JOIN orders o ON o.user_id = a.user_id
		WHERE a.campaign_id = $1 AND `+settledSubscription, id).Scan(&trialPayers); err != nil {
		return AdCampaignStats{}, err
	}
	stats.TrialToPaidPercent = percentOf(trialPayers, stats.Campaign.Trials)

	rows, err := service.pool.Query(ctx, `WITH members AS (
			SELECT user_id, attributed_at FROM customer_campaign_attributions WHERE campaign_id = $1
		), days AS (
			SELECT ((now() AT TIME ZONE 'UTC')::date - offset_days) AS day
			FROM generate_series(0, $2::integer - 1) AS offset_days
		), registrations AS (
			SELECT (attributed_at AT TIME ZONE 'UTC')::date AS day, count(*) AS n FROM members GROUP BY 1
		), trials AS (
			SELECT (t.claimed_at AT TIME ZONE 'UTC')::date AS day, count(*) AS n
			FROM members m JOIN trial_claims t ON t.user_id = m.user_id GROUP BY 1
		), firsts AS (
			SELECT (first.at AT TIME ZONE 'UTC')::date AS day, count(*) AS n
			FROM members m
			JOIN LATERAL (
				SELECT min(COALESCE(o.paid_at, o.created_at)) AS at FROM orders o
				WHERE o.user_id = m.user_id AND `+settledSubscription+`
			) first ON first.at IS NOT NULL
			GROUP BY 1
		), revenue AS (
			SELECT (COALESCE(o.paid_at, o.created_at) AT TIME ZONE 'UTC')::date AS day,
				sum(o.paid_minor - o.refunded_minor) AS n
			FROM members m JOIN orders o ON o.user_id = m.user_id
			WHERE o.currency = $3 AND o.state IN ('paid','fulfilled','partially_refunded','refunded')
			GROUP BY 1
		)
		SELECT to_char(d.day, 'YYYY-MM-DD'), COALESCE(r.n, 0)::bigint, COALESCE(t.n, 0)::bigint,
			COALESCE(f.n, 0)::bigint, COALESCE(v.n, 0)::bigint
		FROM days d
		LEFT JOIN registrations r ON r.day = d.day
		LEFT JOIN trials t ON t.day = d.day
		LEFT JOIN firsts f ON f.day = d.day
		LEFT JOIN revenue v ON v.day = d.day
		ORDER BY d.day`, id, days, stats.Campaign.Currency)
	if err != nil {
		return AdCampaignStats{}, err
	}
	stats.Days, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AdCampaignDay, error) {
		var day AdCampaignDay
		err := row.Scan(&day.Day, &day.Registrations, &day.Trials, &day.FirstPayments, &day.RevenueMinor)
		return day, err
	})
	return stats, err
}

// percentOf is part as a percentage of whole, to one decimal place.
func percentOf(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return math.Round(float64(part)*1000/float64(whole)) / 10
}
