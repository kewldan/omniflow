// Package campaigns queues and resolves campaign recipients.
//
// It does not send anything. The bot's notifier owns delivery, because consent,
// quiet hours, frequency caps, and Telegram delivery health already live there
// and duplicating them here would mean two places that can disagree about
// whether a customer may be messaged.
//
// What this owns is the audience: expanding a segment into per-recipient rows,
// applying the suppression list, and keeping the counters honest. The split
// matters because a campaign's reach and a campaign's delivery fail differently
// — an audience that was wrong is an operator's mistake, and a delivery that
// failed is Telegram's.
package campaigns

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/audience"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// expansionBatch bounds how many recipients one pass queues. A campaign to a
// hundred thousand customers is queued over several passes rather than in one
// statement that holds a connection for minutes.
const expansionBatch = 5_000

// Config tunes the loop. The zero value is the documented default.
type Config struct {
	Interval time.Duration
}

// Runner expands due campaigns into recipients.
type Runner struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	config Config
}

// New builds the campaign runner.
func New(pool *pgxpool.Pool, logger *slog.Logger, config Config) *Runner {
	if config.Interval <= 0 {
		config.Interval = time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{pool: pool, logger: logger, config: config}
}

// Run expands campaigns until the context is cancelled.
func (runner *Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(runner.config.Interval)
	defer ticker.Stop()
	for {
		runner.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce advances every campaign that is due or running.
func (runner *Runner) RunOnce(ctx context.Context) {
	queries := dbgen.New(runner.pool)
	campaigns, err := queries.ListDueCampaigns(ctx, 20)
	if err != nil {
		runner.logger.Error("campaign lookup failed", "error", err)
		return
	}
	for _, campaign := range campaigns {
		if ctx.Err() != nil {
			return
		}
		if err := runner.advance(ctx, queries, campaign); err != nil {
			runner.logger.Error("campaign expansion failed",
				"campaignId", uuidString(campaign.ID), "error", err)
		}
	}
	runner.publishScheduledNews(ctx, queries)
}

// advance moves one campaign forward.
//
// A scheduled campaign whose time has come starts running; a running campaign
// queues the next batch of its audience. Both happen here rather than at the
// operator's click, so a campaign to a large audience does not depend on a
// request staying open.
func (runner *Runner) advance(
	ctx context.Context, queries *dbgen.Queries, campaign dbgen.Campaign,
) error {
	if campaign.Status == "scheduled" {
		started, err := queries.SetCampaignState(ctx, dbgen.SetCampaignStateParams{
			CampaignID: campaign.ID, Status: "running",
			AllowedFrom: []string{"scheduled"},
		})
		if err != nil {
			return err
		}
		campaign = started
	}

	detail, err := queries.GetCampaign(ctx, campaign.ID)
	if err != nil {
		return err
	}
	filters := map[string]any{}
	if err = json.Unmarshal(detail.Filters, &filters); err != nil {
		return err
	}
	query, err := audience.Compile(filters, time.Now().UTC())
	if err != nil {
		// A segment that no longer compiles cannot be sent to. The campaign is
		// paused rather than left running against an audience nobody can read.
		runner.logger.Error("campaign segment is unreadable",
			"campaignId", uuidString(campaign.ID), "error", err)
		_, pauseErr := queries.SetCampaignState(ctx, dbgen.SetCampaignStateParams{
			CampaignID: campaign.ID, Status: "paused", AllowedFrom: []string{"running"},
		})
		return pauseErr
	}

	queued, err := runner.queueBatch(ctx, queries, campaign.ID, query)
	if err != nil {
		return err
	}
	if _, err = queries.RecountCampaign(ctx, campaign.ID); err != nil {
		return err
	}

	// A pass that queued nobody new has walked the whole audience. Whether the
	// campaign is finished depends on the recipients still waiting for the bot,
	// which is why completion is decided from the counters rather than from
	// this pass.
	if queued == 0 {
		recounted, err := queries.RecountCampaign(ctx, campaign.ID)
		if err != nil {
			return err
		}
		if recounted.QueuedCount == 0 {
			_, err = queries.SetCampaignState(ctx, dbgen.SetCampaignStateParams{
				CampaignID: campaign.ID, Status: "completed", AllowedFrom: []string{"running"},
			})
			return err
		}
	}
	return nil
}

// queueBatch writes recipient rows for the next slice of the audience.
//
// Suppressed customers are queued and immediately resolved as suppressed rather
// than skipped silently. An operator reviewing reach needs to see that four
// hundred people were on the list and not contacted, and why — a campaign that
// simply reports a smaller audience hides the decision.
func (runner *Runner) queueBatch(
	ctx context.Context, queries *dbgen.Queries, campaignID pgtype.UUID, query audience.Query,
) (int, error) {
	args := append([]any{campaignID}, query.Args...)
	rows, err := runner.pool.Query(ctx, `
		SELECT u.id,
		       EXISTS (SELECT 1 FROM communication_suppressions s WHERE s.user_id = u.id)
		FROM users u
		WHERE u.status = 'active'
		  AND (`+query.Where+`)
		  AND NOT EXISTS (
			SELECT 1 FROM campaign_recipients r
			WHERE r.campaign_id = $1 AND r.user_id = u.id
		  )
		ORDER BY u.id
		LIMIT `+itoa(expansionBatch), args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type candidate struct {
		id         pgtype.UUID
		suppressed bool
	}
	candidates := make([]candidate, 0, expansionBatch)
	for rows.Next() {
		var found candidate
		if err := rows.Scan(&found.id, &found.suppressed); err != nil {
			return 0, err
		}
		candidates = append(candidates, found)
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}

	for _, found := range candidates {
		if err = queries.QueueCampaignRecipient(ctx, dbgen.QueueCampaignRecipientParams{
			CampaignID: campaignID, UserID: found.id,
		}); err != nil {
			return 0, err
		}
		if found.suppressed {
			if err = queries.ResolveCampaignRecipient(ctx, dbgen.ResolveCampaignRecipientParams{
				CampaignID: campaignID, UserID: found.id, Status: "suppressed",
				SuppressionReason: pgtype.Text{String: "suppressed", Valid: true},
			}); err != nil {
				return 0, err
			}
		}
	}
	return len(candidates), nil
}

// publishScheduledNews publishes posts whose time has come.
//
// It lives here rather than in its own loop because it is the same shape of
// work — something an operator scheduled, becoming visible on time — and one
// sweep is easier to reason about than two.
func (runner *Runner) publishScheduledNews(ctx context.Context, queries *dbgen.Queries) {
	posts, err := queries.ListDueNewsPosts(ctx, 50)
	if err != nil {
		runner.logger.Error("scheduled news lookup failed", "error", err)
		return
	}
	for _, post := range posts {
		if _, err := queries.SetNewsPostState(ctx, dbgen.SetNewsPostStateParams{
			PostID: post.ID, Status: "published",
		}); err != nil {
			runner.logger.Error("scheduled publication failed",
				"postId", uuidString(post.ID), "error", err)
		}
	}
}

func uuidString(id pgtype.UUID) string {
	value, err := id.Value()
	if err != nil || value == nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

// itoa avoids importing strconv for one constant that is compiled in.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
