// Package referralplus adds a second referral level and referral contests on
// top of the existing referral programme, as decision 0009 requires: every
// payout is a ledger posting, never a second balance.
//
// Level two: when an inviter is credited a money reward, the inviter's own
// inviter receives a share of it. A periodic pass grants it, keyed by the
// first-level reward so it is paid once, and reverses it when the
// first-level reward is reversed. Day rewards have no second level.
//
// Contests: an operator sets a window and prizes per place; the leaderboard
// counts each inviter's referrals that qualified in the window and were not
// rejected by the referral review. Prizes are paid when the operator finishes
// the contest, once, with an audit row.
package referralplus

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/walletpost"
)

// Errors the panel maps.
var (
	ErrInvalid  = errors.New("the request is not valid")
	ErrNotFound = errors.New("no such contest")
	ErrState    = errors.New("the contest cannot be finished now")
)

// LevelSettings is the second-level configuration.
type LevelSettings struct {
	Enabled bool `json:"enabled"`
	Percent int  `json:"percent"`
}

// Service is the adapter.
type Service struct {
	pool *pgxpool.Pool
}

// New builds the service.
func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// LevelSettings reads the configuration.
func (service *Service) LevelSettings(ctx context.Context) (LevelSettings, error) {
	var settings LevelSettings
	err := service.pool.QueryRow(ctx, `SELECT level2_enabled, level2_percent FROM referral_level_settings`).
		Scan(&settings.Enabled, &settings.Percent)
	return settings, err
}

// SaveLevelSettings stores it and audits the change.
func (service *Service) SaveLevelSettings(ctx context.Context, settings LevelSettings, actor panelpg.Actor) error {
	if settings.Percent < 1 || settings.Percent > 50 {
		return ErrInvalid
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `UPDATE referral_level_settings SET level2_enabled = $1, level2_percent = $2,
		updated_at = now(), updated_by = NULLIF($3, '')::uuid`, settings.Enabled, settings.Percent, actor.AdminID); err != nil {
		return err
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.referral.level2_saved", "marketing", "referral_program", "level2",
		map[string]any{"enabled": settings.Enabled, "percent": settings.Percent}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Pass grants new second-level rewards and reverses those whose first-level
// reward was reversed. It is safe to repeat.
func (service *Service) Pass(ctx context.Context) error {
	settings, err := service.LevelSettings(ctx)
	if err != nil {
		return err
	}
	if settings.Enabled {
		if err = service.grant(ctx, settings); err != nil {
			return err
		}
	}
	// Reversal runs even when the level is switched off: money paid on a
	// reward that turned out to be abuse comes back either way.
	return service.reverse(ctx)
}

func (service *Service) grant(ctx context.Context, settings LevelSettings) error {
	rows, err := service.pool.Query(ctx, `SELECT r.id::text, upper.referrer_user_id::text, r.amount_minor, r.currency
		FROM referral_rewards r
		JOIN referral_attributions upper ON upper.referred_user_id = r.beneficiary_user_id AND upper.review_state <> 'rejected'
		WHERE r.role = 'inviter' AND r.days IS NULL AND r.reversed_at IS NULL
		  AND r.granted_at >= (SELECT updated_at FROM referral_level_settings) - interval '1 day'
		  AND upper.referrer_user_id <> r.referred_user_id
		  AND NOT EXISTS (SELECT 1 FROM referral_level2_rewards l WHERE l.source_reward_id = r.id)
		LIMIT 200`)
	if err != nil {
		return err
	}
	type pending struct {
		reward, beneficiary, currency string
		amount                        int64
	}
	var batch []pending
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.reward, &item.beneficiary, &item.amount, &item.currency); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, item)
	}
	rows.Close()
	for _, item := range batch {
		share := item.amount * int64(settings.Percent) / 100
		if share <= 0 {
			continue
		}
		if err = service.pay(ctx, item.reward, item.beneficiary, item.currency, share); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (service *Service) pay(ctx context.Context, reward, beneficiary, currency string, amount int64) error {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	transaction, err := walletpost.Post(ctx, tx, walletpost.Posting{
		Type: "referral_reward", ReferenceType: "referral_level2", ReferenceID: reward,
		IdempotencyKey: "referral-level2:" + reward, Reason: "Second-level referral reward",
		ActorID: "system", CustomerID: beneficiary, Currency: currency, AmountMinor: amount,
	})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO referral_level2_rewards (source_reward_id, beneficiary_user_id, amount_minor, currency, ledger_transaction_id)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5::uuid) ON CONFLICT (source_reward_id) DO NOTHING`,
		reward, beneficiary, amount, currency, transaction); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (service *Service) reverse(ctx context.Context) error {
	rows, err := service.pool.Query(ctx, `SELECT l.source_reward_id::text, l.beneficiary_user_id::text, l.amount_minor, l.currency
		FROM referral_level2_rewards l JOIN referral_rewards r ON r.id = l.source_reward_id
		WHERE r.reversed_at IS NOT NULL AND l.reversed_at IS NULL LIMIT 200`)
	if err != nil {
		return err
	}
	type reversal struct {
		reward, beneficiary, currency string
		amount                        int64
	}
	var batch []reversal
	for rows.Next() {
		var item reversal
		if err = rows.Scan(&item.reward, &item.beneficiary, &item.amount, &item.currency); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, item)
	}
	rows.Close()
	for _, item := range batch {
		tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			return err
		}
		// The correction may take the balance negative, as the first-level
		// reversal does: the money was paid on a referral that was abuse.
		transaction, err := walletpost.Post(ctx, tx, walletpost.Posting{
			Type: "correction", ReferenceType: "referral_level2", ReferenceID: item.reward,
			IdempotencyKey: "referral-level2-reversal:" + item.reward, Reason: "Second-level referral reward reversed",
			ActorID: "system", CustomerID: item.beneficiary, Currency: item.currency, AmountMinor: -item.amount,
		})
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE referral_level2_rewards SET reversed_at = now(), reversal_ledger_transaction_id = $2::uuid
				WHERE source_reward_id = $1::uuid AND reversed_at IS NULL`, item.reward, transaction)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		_ = tx.Rollback(ctx)
		if err != nil {
			return err
		}
	}
	return rows.Err()
}

// Contest is one referral contest.
type Contest struct {
	ID         string     `json:"id"`
	TitleRU    string     `json:"titleRu"`
	TitleEN    string     `json:"titleEn"`
	StartsAt   time.Time  `json:"startsAt"`
	EndsAt     time.Time  `json:"endsAt"`
	Currency   string     `json:"currency"`
	Prizes     []int64    `json:"prizes"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Standing is one inviter's place.
type Standing struct {
	Place      int    `json:"place"`
	CustomerID string `json:"customerId,omitempty"`
	Referrals  int    `json:"referrals"`
	You        bool   `json:"you,omitempty"`
}

// CreateContest opens a contest.
func (service *Service) CreateContest(ctx context.Context, contest Contest, actor panelpg.Actor) (Contest, error) {
	contest.TitleRU, contest.TitleEN = strings.TrimSpace(contest.TitleRU), strings.TrimSpace(contest.TitleEN)
	contest.Currency = strings.ToUpper(strings.TrimSpace(contest.Currency))
	if contest.Currency == "" {
		contest.Currency = "RUB"
	}
	valid := contest.TitleRU != "" && contest.TitleEN != "" && utf8.RuneCountInString(contest.TitleRU) <= 120 &&
		utf8.RuneCountInString(contest.TitleEN) <= 120 && contest.EndsAt.After(contest.StartsAt) &&
		len(contest.Prizes) >= 1 && len(contest.Prizes) <= 10 && len(contest.Currency) == 3
	for _, prize := range contest.Prizes {
		valid = valid && prize > 0
	}
	if !valid {
		return Contest{}, ErrInvalid
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Contest{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = tx.QueryRow(ctx, `INSERT INTO referral_contests (title_ru, title_en, starts_at, ends_at, currency, prizes, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')::uuid) RETURNING id::text`, contest.TitleRU, contest.TitleEN,
		contest.StartsAt, contest.EndsAt, contest.Currency, contest.Prizes, actor.AdminID).Scan(&contest.ID); err != nil {
		return Contest{}, err
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.referral.contest_created", "marketing", "referral_contest", contest.ID,
		map[string]any{"prizes": contest.Prizes, "currency": contest.Currency}); err != nil {
		return Contest{}, err
	}
	return contest, tx.Commit(ctx)
}

// Contests lists the contests, newest first. activeOnly keeps those running now.
func (service *Service) Contests(ctx context.Context, activeOnly bool) ([]Contest, error) {
	rows, err := service.pool.Query(ctx, `SELECT id::text, title_ru, title_en, starts_at, ends_at, currency, prizes, finished_at
		FROM referral_contests WHERE NOT $1 OR (finished_at IS NULL AND starts_at <= now() AND ends_at > now())
		ORDER BY created_at DESC LIMIT 20`, activeOnly)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Contest, error) {
		var contest Contest
		err := row.Scan(&contest.ID, &contest.TitleRU, &contest.TitleEN, &contest.StartsAt, &contest.EndsAt,
			&contest.Currency, &contest.Prizes, &contest.FinishedAt)
		return contest, err
	})
}

type rowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Leaderboard ranks inviters; ties go to whoever reached the count first.
func (service *Service) Leaderboard(ctx context.Context, contestID string, limit int) ([]Standing, error) {
	return leaderboard(ctx, service.pool, contestID, limit)
}

func leaderboard(ctx context.Context, db rowQuerier, contestID string, limit int) ([]Standing, error) {
	rows, err := db.Query(ctx, `SELECT a.referrer_user_id::text, count(*)::integer
		FROM referral_contests c
		JOIN referral_attributions a ON a.qualified_at >= c.starts_at AND a.qualified_at < c.ends_at
			AND a.review_state <> 'rejected'
		WHERE c.id = $1::uuid
		GROUP BY a.referrer_user_id
		ORDER BY count(*) DESC, max(a.qualified_at) ASC
		LIMIT $2`, contestID, limit)
	if err != nil {
		return nil, err
	}
	place := 0
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Standing, error) {
		var standing Standing
		place++
		standing.Place = place
		err := row.Scan(&standing.CustomerID, &standing.Referrals)
		return standing, err
	})
}

// CustomerView is a customer's leaderboard: places without other customers'
// identities, and their own place marked.
func (service *Service) CustomerView(ctx context.Context, customerID, contestID string) ([]Standing, error) {
	standings, err := service.Leaderboard(ctx, contestID, 100)
	if err != nil {
		return nil, err
	}
	shown := []Standing{}
	for _, standing := range standings {
		mine := standing.CustomerID == customerID
		if standing.Place <= 10 || mine {
			shown = append(shown, Standing{Place: standing.Place, Referrals: standing.Referrals, You: mine})
		}
	}
	return shown, nil
}

// FinishContest pays the places once the contest has ended and audits it.
// Finishing twice is refused, so no prize is paid twice.
func (service *Service) FinishContest(ctx context.Context, contestID string, actor panelpg.Actor) ([]Standing, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var contest Contest
	err = tx.QueryRow(ctx, `SELECT currency, prizes, ends_at, finished_at FROM referral_contests WHERE id = $1::uuid FOR UPDATE`,
		contestID).Scan(&contest.Currency, &contest.Prizes, &contest.EndsAt, &contest.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, ErrNotFound
	}
	if contest.FinishedAt != nil || time.Now().Before(contest.EndsAt) {
		return nil, ErrState
	}
	standings, err := leaderboard(ctx, tx, contestID, len(contest.Prizes))
	if err != nil {
		return nil, err
	}
	for _, standing := range standings {
		amount := contest.Prizes[standing.Place-1]
		transaction, err := walletpost.Post(ctx, tx, walletpost.Posting{
			Type: "credit", ReferenceType: "referral_contest", ReferenceID: contestID + ":" + itoa(standing.Place),
			IdempotencyKey: "referral-contest:" + contestID + ":" + itoa(standing.Place), Reason: "Referral contest prize",
			ActorID: actor.AdminID, CustomerID: standing.CustomerID, Currency: contest.Currency, AmountMinor: amount,
		})
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO referral_contest_winners (contest_id, place, user_id, referrals, amount_minor, ledger_transaction_id)
			VALUES ($1::uuid, $2, $3::uuid, $4, $5, $6::uuid)`, contestID, standing.Place, standing.CustomerID,
			standing.Referrals, amount, transaction); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE referral_contests SET finished_at = now(), finished_by = NULLIF($2, '')::uuid WHERE id = $1::uuid`,
		contestID, actor.AdminID); err != nil {
		return nil, err
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.referral.contest_finished", "financial", "referral_contest", contestID,
		map[string]any{"winners": len(standings)}); err != nil {
		return nil, err
	}
	return standings, tx.Commit(ctx)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
