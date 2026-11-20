package engagement

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/walletpost"
)

// Errors a customer or operator acts on.
var (
	ErrDisabled     = errors.New("the wheel is not available")
	ErrLimit        = errors.New("no spins are left today")
	ErrTooNew       = errors.New("the account is too new")
	ErrInsufficient = errors.New("the wallet does not cover the spin")
	ErrInvalid      = errors.New("the request is not valid")
	ErrRTP          = errors.New("the prize table pays out more than the target return")
	ErrNotFound     = errors.New("not found")
	ErrAnswered     = errors.New("the poll was already answered")
	ErrClosed       = errors.New("the poll is not open")
)

// Settings is the operator's configuration.
type Settings struct {
	Currency           string `json:"currency"`
	DaysPlanVersionID  string `json:"daysPlanVersionId"`
	DayValueMinor      int64  `json:"dayValueMinor"`
	WheelEnabled       bool   `json:"wheelEnabled"`
	SpinCostMinor      int64  `json:"spinCostMinor"`
	FreeSpinsPerDay    int    `json:"freeSpinsPerDay"`
	MaxSpinsPerDay     int    `json:"maxSpinsPerDay"`
	TargetRTPPercent   int    `json:"targetRtpPercent"`
	MinimumAccountAgeH int    `json:"minimumAccountAgeHours"`
}

// Service is the engagement adapter.
type Service struct {
	pool   *pgxpool.Pool
	orders *commercepg.Store
	clock  func() time.Time
}

// New builds the service.
func New(pool *pgxpool.Pool, orders *commercepg.Store) *Service {
	return &Service{pool: pool, orders: orders, clock: time.Now}
}

type rower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func readSettings(ctx context.Context, db rower) (Settings, error) {
	var settings Settings
	err := db.QueryRow(ctx, `SELECT currency, COALESCE(days_plan_version_id::text, ''), day_value_minor, wheel_enabled,
			wheel_spin_cost_minor, wheel_free_spins_per_day, wheel_max_spins_per_day, wheel_target_rtp_percent,
			minimum_account_age_hours
		FROM engagement_settings`).Scan(&settings.Currency, &settings.DaysPlanVersionID, &settings.DayValueMinor,
		&settings.WheelEnabled, &settings.SpinCostMinor, &settings.FreeSpinsPerDay, &settings.MaxSpinsPerDay,
		&settings.TargetRTPPercent, &settings.MinimumAccountAgeH)
	return settings, err
}

// Settings reads the configuration.
func (service *Service) Settings(ctx context.Context) (Settings, error) {
	return readSettings(ctx, service.pool)
}

// Prizes lists the wheel's prize table.
func (service *Service) Prizes(ctx context.Context) ([]Prize, error) {
	return prizes(ctx, service.pool, false)
}

func prizes(ctx context.Context, db interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, activeOnly bool) ([]Prize, error) {
	rows, err := db.Query(ctx, `SELECT id::text, label_ru, label_en, kind, value, weight, active FROM wheel_prizes
		WHERE (NOT $1 OR active) ORDER BY created_at`, activeOnly)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Prize, error) {
		var prize Prize
		err := row.Scan(&prize.ID, &prize.LabelRU, &prize.LabelEN, &prize.Kind, &prize.Value, &prize.Weight, &prize.Active)
		return prize, err
	})
}

// SaveWheel replaces the settings and the prize table together, refusing a
// table whose expected payout is above the target return for a paid spin.
func (service *Service) SaveWheel(ctx context.Context, settings Settings, table []Prize, actor panelpg.Actor) error {
	settings.Currency = strings.ToUpper(strings.TrimSpace(settings.Currency))
	if len(settings.Currency) != 3 || settings.DayValueMinor <= 0 || settings.SpinCostMinor < 0 ||
		settings.FreeSpinsPerDay < 0 || settings.FreeSpinsPerDay > 10 || settings.MaxSpinsPerDay < 1 ||
		settings.MaxSpinsPerDay > 100 || settings.FreeSpinsPerDay > settings.MaxSpinsPerDay ||
		settings.TargetRTPPercent < 1 || settings.TargetRTPPercent > 100 ||
		settings.MinimumAccountAgeH < 0 || settings.MinimumAccountAgeH > 8760 || len(table) > 24 {
		return ErrInvalid
	}
	hasDays := false
	for _, prize := range table {
		if err := prize.Validate(); err != nil {
			return ErrInvalid
		}
		hasDays = hasDays || (prize.Active && prize.Kind == KindDays)
	}
	if hasDays && settings.DaysPlanVersionID == "" {
		return ErrInvalid
	}
	if settings.WheelEnabled && ExpectedValue(table, settings.DayValueMinor) == 0 && len(table) == 0 {
		return ErrInvalid
	}
	if settings.SpinCostMinor > 0 && RTPPercent(table, settings.DayValueMinor, settings.SpinCostMinor) > float64(settings.TargetRTPPercent) {
		return ErrRTP
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `UPDATE engagement_settings SET currency = $1, days_plan_version_id = NULLIF($2, '')::uuid,
			day_value_minor = $3, wheel_enabled = $4, wheel_spin_cost_minor = $5, wheel_free_spins_per_day = $6,
			wheel_max_spins_per_day = $7, wheel_target_rtp_percent = $8, minimum_account_age_hours = $9,
			updated_at = now(), updated_by = NULLIF($10, '')::uuid`,
		settings.Currency, settings.DaysPlanVersionID, settings.DayValueMinor, settings.WheelEnabled, settings.SpinCostMinor,
		settings.FreeSpinsPerDay, settings.MaxSpinsPerDay, settings.TargetRTPPercent, settings.MinimumAccountAgeH,
		actor.AdminID); err != nil {
		return ErrInvalid
	}
	// Prizes already won keep their rows; a removed prize is deactivated,
	// never deleted, so every spin still names what it paid.
	if _, err = tx.Exec(ctx, `UPDATE wheel_prizes SET active = false`); err != nil {
		return err
	}
	for _, prize := range table {
		if _, err = tx.Exec(ctx, `INSERT INTO wheel_prizes (label_ru, label_en, kind, value, weight, active)
			VALUES ($1, $2, $3, $4, $5, $6)`, prize.LabelRU, prize.LabelEN, prize.Kind, prize.Value, prize.Weight, prize.Active); err != nil {
			return err
		}
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.engagement.wheel_saved", "marketing", "wheel", "settings",
		map[string]any{
			"enabled": settings.WheelEnabled, "spinCostMinor": settings.SpinCostMinor, "prizes": len(table),
			"rtpPercent":     RTPPercent(table, settings.DayValueMinor, settings.SpinCostMinor),
			"maxSpinsPerDay": settings.MaxSpinsPerDay,
		}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// WheelState is what the customer's wheel screen shows.
type WheelState struct {
	Enabled        bool    `json:"enabled"`
	CostMinor      int64   `json:"costMinor"`
	Currency       string  `json:"currency"`
	FreeLeft       int     `json:"freeLeft"`
	SpinsLeft      int     `json:"spinsLeft"`
	Prizes         []Prize `json:"prizes"`
	RecentOutcomes []Spin  `json:"recent"`
}

// Spin is one spin's outcome.
type Spin struct {
	ID        string    `json:"id"`
	Prize     Prize     `json:"prize"`
	CostMinor int64     `json:"costMinor"`
	Free      bool      `json:"free"`
	CreatedAt time.Time `json:"createdAt"`
}

func (service *Service) spinsToday(ctx context.Context, db rower, customerID string) (total, free int, err error) {
	err = db.QueryRow(ctx, `SELECT count(*)::integer, count(*) FILTER (WHERE free)::integer FROM wheel_spins
		WHERE user_id = $1::uuid AND created_at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`,
		customerID).Scan(&total, &free)
	return total, free, err
}

// Wheel reads the customer's wheel.
func (service *Service) Wheel(ctx context.Context, customerID string) (WheelState, error) {
	settings, err := service.Settings(ctx)
	if err != nil {
		return WheelState{}, err
	}
	state := WheelState{Enabled: settings.WheelEnabled, CostMinor: settings.SpinCostMinor, Currency: settings.Currency,
		Prizes: []Prize{}, RecentOutcomes: []Spin{}}
	if !settings.WheelEnabled {
		return state, nil
	}
	total, free, err := service.spinsToday(ctx, service.pool, customerID)
	if err != nil {
		return WheelState{}, err
	}
	state.SpinsLeft = max(settings.MaxSpinsPerDay-total, 0)
	state.FreeLeft = min(max(settings.FreeSpinsPerDay-free, 0), state.SpinsLeft)
	if state.Prizes, err = prizes(ctx, service.pool, true); err != nil {
		return WheelState{}, err
	}
	rows, err := service.pool.Query(ctx, `SELECT s.id::text, s.prize_kind, s.prize_value, s.cost_minor, s.free, s.created_at,
			COALESCE(p.label_ru, ''), COALESCE(p.label_en, '')
		FROM wheel_spins s LEFT JOIN wheel_prizes p ON p.id = s.prize_id
		WHERE s.user_id = $1::uuid ORDER BY s.created_at DESC LIMIT 10`, customerID)
	if err != nil {
		return WheelState{}, err
	}
	state.RecentOutcomes, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Spin, error) {
		var spin Spin
		err := row.Scan(&spin.ID, &spin.Prize.Kind, &spin.Prize.Value, &spin.CostMinor, &spin.Free, &spin.CreatedAt,
			&spin.Prize.LabelRU, &spin.Prize.LabelEN)
		return spin, err
	})
	return state, err
}

// SpinWheel spins once. The charge, the draw, the payout, and the spin row
// commit together under serializable isolation; a replay with the same key
// returns the spin already made.
func (service *Service) SpinWheel(ctx context.Context, customerID, key string) (Spin, error) {
	key = strings.TrimSpace(key)
	if len(key) < 8 || len(key) > 128 {
		return Spin{}, ErrInvalid
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return Spin{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var existing Spin
	err = tx.QueryRow(ctx, `SELECT s.id::text, s.prize_kind, s.prize_value, s.cost_minor, s.free, s.created_at,
			COALESCE(p.label_ru, ''), COALESCE(p.label_en, '')
		FROM wheel_spins s LEFT JOIN wheel_prizes p ON p.id = s.prize_id
		WHERE s.user_id = $1::uuid AND s.idempotency_key = $2`, customerID, key).Scan(&existing.ID, &existing.Prize.Kind,
		&existing.Prize.Value, &existing.CostMinor, &existing.Free, &existing.CreatedAt, &existing.Prize.LabelRU, &existing.Prize.LabelEN)
	if err == nil {
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Spin{}, err
	}
	settings, err := readSettings(ctx, tx)
	if err != nil {
		return Spin{}, err
	}
	if !settings.WheelEnabled {
		return Spin{}, ErrDisabled
	}
	if err = service.oldEnough(ctx, tx, customerID, settings); err != nil {
		return Spin{}, err
	}
	total, free, err := service.spinsToday(ctx, tx, customerID)
	if err != nil {
		return Spin{}, err
	}
	if total >= settings.MaxSpinsPerDay {
		return Spin{}, ErrLimit
	}
	isFree := free < settings.FreeSpinsPerDay || settings.SpinCostMinor == 0
	cost := int64(0)
	if !isFree {
		cost = settings.SpinCostMinor
	}
	table, err := prizes(ctx, tx, true)
	if err != nil {
		return Spin{}, err
	}
	prize, err := Draw(table)
	if err != nil {
		return Spin{}, ErrDisabled
	}
	var spin Spin
	if err = tx.QueryRow(ctx, `INSERT INTO wheel_spins (user_id, prize_id, prize_kind, prize_value, cost_minor, currency, free, idempotency_key)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8) RETURNING id::text, created_at`,
		customerID, prize.ID, prize.Kind, prize.Value, cost, settings.Currency, isFree, key).Scan(&spin.ID, &spin.CreatedAt); err != nil {
		return Spin{}, err
	}
	spin.Prize, spin.CostMinor, spin.Free = prize, cost, isFree
	if cost > 0 {
		if _, err = walletpost.Post(ctx, tx, walletpost.Posting{
			Type: "debit", ReferenceType: "wheel_spin", ReferenceID: spin.ID, IdempotencyKey: "wheel-spin:" + spin.ID,
			Reason: "Fortune wheel spin", ActorID: customerID, CustomerID: customerID, Currency: settings.Currency,
			AmountMinor: -cost, CheckBalance: true,
		}); err != nil {
			if errors.Is(err, walletpost.ErrInsufficient) {
				return Spin{}, ErrInsufficient
			}
			return Spin{}, err
		}
	}
	if err = service.pay(ctx, tx, customerID, settings, prize.Kind, prize.Value, "wheel-prize:"+spin.ID, "wheel_prize", spin.ID); err != nil {
		return Spin{}, err
	}
	return spin, tx.Commit(ctx)
}

func (service *Service) oldEnough(ctx context.Context, tx pgx.Tx, customerID string, settings Settings) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT status = 'active' AND created_at <= now() - make_interval(hours => $2)
		FROM users WHERE id = $1::uuid`, customerID, settings.MinimumAccountAgeH).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrTooNew
	}
	return nil
}

// pay grants a prize or a reward.
func (service *Service) pay(ctx context.Context, tx pgx.Tx, customerID string, settings Settings, kind string, value int64, key, referenceType, referenceID string) error {
	switch kind {
	case KindWallet:
		_, err := walletpost.Post(ctx, tx, walletpost.Posting{
			Type: "credit", ReferenceType: referenceType, ReferenceID: referenceID, IdempotencyKey: key,
			Reason: "Engagement prize", ActorID: customerID, CustomerID: customerID, Currency: settings.Currency,
			AmountMinor: value,
		})
		return err
	case KindDays:
		if settings.DaysPlanVersionID == "" || service.orders == nil {
			return ErrInvalid
		}
		_, err := service.orders.GrantDays(ctx, tx, customerID, settings.DaysPlanVersionID, settings.Currency, int(value), key)
		return err
	default:
		return nil
	}
}

// Poll is one poll.
type Poll struct {
	ID          string     `json:"id"`
	QuestionRU  string     `json:"questionRu"`
	QuestionEN  string     `json:"questionEn"`
	Options     []Option   `json:"options"`
	RewardKind  string     `json:"rewardKind"`
	RewardValue int64      `json:"rewardValue"`
	StartsAt    time.Time  `json:"startsAt"`
	EndsAt      time.Time  `json:"endsAt"`
	ClosedAt    *time.Time `json:"closedAt,omitempty"`
	Answered    *int       `json:"answered,omitempty"`
	Counts      []int      `json:"counts,omitempty"`
}

// Option is one answer.
type Option struct {
	RU string `json:"ru"`
	EN string `json:"en"`
}

// CreatePoll opens a poll.
func (service *Service) CreatePoll(ctx context.Context, poll Poll, actor panelpg.Actor) (Poll, error) {
	poll.QuestionRU, poll.QuestionEN = strings.TrimSpace(poll.QuestionRU), strings.TrimSpace(poll.QuestionEN)
	if poll.RewardKind == "" {
		poll.RewardKind = "none"
	}
	if poll.StartsAt.IsZero() {
		poll.StartsAt = service.clock().UTC()
	}
	valid := poll.QuestionRU != "" && poll.QuestionEN != "" && utf8.RuneCountInString(poll.QuestionRU) <= 300 &&
		utf8.RuneCountInString(poll.QuestionEN) <= 300 && len(poll.Options) >= 2 && len(poll.Options) <= 10 &&
		poll.EndsAt.After(poll.StartsAt) &&
		((poll.RewardKind == "none" && poll.RewardValue == 0) || (poll.RewardKind == KindWallet && poll.RewardValue > 0) ||
			(poll.RewardKind == KindDays && poll.RewardValue >= 1 && poll.RewardValue <= 30))
	for _, option := range poll.Options {
		valid = valid && strings.TrimSpace(option.RU) != "" && strings.TrimSpace(option.EN) != "" &&
			utf8.RuneCountInString(option.RU) <= 100 && utf8.RuneCountInString(option.EN) <= 100
	}
	if !valid {
		return Poll{}, ErrInvalid
	}
	options, err := json.Marshal(poll.Options)
	if err != nil {
		return Poll{}, err
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Poll{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = tx.QueryRow(ctx, `INSERT INTO polls (question_ru, question_en, options, reward_kind, reward_value, starts_at, ends_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::uuid) RETURNING id::text`, poll.QuestionRU, poll.QuestionEN, options,
		poll.RewardKind, poll.RewardValue, poll.StartsAt, poll.EndsAt, actor.AdminID).Scan(&poll.ID); err != nil {
		return Poll{}, err
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.engagement.poll_created", "marketing", "poll", poll.ID,
		map[string]any{"rewardKind": poll.RewardKind, "rewardValue": poll.RewardValue, "options": len(poll.Options)}); err != nil {
		return Poll{}, err
	}
	return poll, tx.Commit(ctx)
}

// ClosePoll ends a poll early.
func (service *Service) ClosePoll(ctx context.Context, pollID string, actor panelpg.Actor) error {
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE polls SET closed_at = now() WHERE id = $1::uuid AND closed_at IS NULL`, pollID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.engagement.poll_closed", "marketing", "poll", pollID, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Polls lists polls: every poll with its counts for the operator, or the
// open ones with the customer's own answer for a customer.
func (service *Service) Polls(ctx context.Context, customerID string) ([]Poll, error) {
	rows, err := service.pool.Query(ctx, `SELECT p.id::text, p.question_ru, p.question_en, p.options, p.reward_kind,
			p.reward_value, p.starts_at, p.ends_at, p.closed_at,
			(SELECT a.option_index FROM poll_answers a WHERE a.poll_id = p.id AND a.user_id = NULLIF($1, '')::uuid),
			(SELECT COALESCE(jsonb_agg(c ORDER BY i), '[]') FROM (
				SELECT i, (SELECT count(*) FROM poll_answers a WHERE a.poll_id = p.id AND a.option_index = i) AS c
				FROM generate_series(0, jsonb_array_length(p.options) - 1) i) counts)
		FROM polls p
		WHERE $1 = '' OR (p.closed_at IS NULL AND p.starts_at <= now() AND p.ends_at > now())
		ORDER BY p.created_at DESC LIMIT 50`, customerID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Poll, error) {
		var poll Poll
		var options, counts []byte
		err := row.Scan(&poll.ID, &poll.QuestionRU, &poll.QuestionEN, &options, &poll.RewardKind, &poll.RewardValue,
			&poll.StartsAt, &poll.EndsAt, &poll.ClosedAt, &poll.Answered, &counts)
		if err == nil {
			err = json.Unmarshal(options, &poll.Options)
		}
		if err == nil {
			err = json.Unmarshal(counts, &poll.Counts)
		}
		return poll, err
	})
	if customerID != "" {
		// Customers do not see how others answered.
		for index := range list {
			list[index].Counts = nil
		}
	}
	return list, err
}

// Answer records a customer's answer and pays the reward once. The answer
// row's primary key is what makes a second answer, or a replayed one, fail.
func (service *Service) Answer(ctx context.Context, customerID, pollID string, option int) (bool, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var poll Poll
	var options []byte
	err = tx.QueryRow(ctx, `SELECT options, reward_kind, reward_value FROM polls
		WHERE id = $1::uuid AND closed_at IS NULL AND starts_at <= now() AND ends_at > now()`, pollID).
		Scan(&options, &poll.RewardKind, &poll.RewardValue)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrClosed
	}
	if err != nil {
		return false, ErrClosed
	}
	if err = json.Unmarshal(options, &poll.Options); err != nil {
		return false, err
	}
	if option < 0 || option >= len(poll.Options) {
		return false, ErrInvalid
	}
	settings, err := readSettings(ctx, tx)
	if err != nil {
		return false, err
	}
	// A reward is paid only to an account old enough to count; a younger one
	// may still answer.
	rewarded := poll.RewardKind != "none" && service.oldEnough(ctx, tx, customerID, settings) == nil
	if _, err = tx.Exec(ctx, `INSERT INTO poll_answers (poll_id, user_id, option_index, rewarded) VALUES ($1::uuid, $2::uuid, $3, $4)`,
		pollID, customerID, option, rewarded); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return false, ErrAnswered
		}
		return false, err
	}
	if rewarded {
		if err = service.pay(ctx, tx, customerID, settings, poll.RewardKind, poll.RewardValue,
			"poll-reward:"+pollID+":"+customerID, "poll_reward", pollID+":"+customerID); err != nil {
			return false, err
		}
	}
	return rewarded, tx.Commit(ctx)
}
