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
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/walletpost"
)

// Daily game errors a customer or an operator acts on.
var (
	ErrGameDisabled = errors.New("the game is not available")
	// ErrGameClosed reports a game that has paid out what it may today, or a
	// first-to-answer round whose prizes are taken.
	ErrGameClosed = errors.New("the game is closed for today")
	// ErrNoQuestion reports a quiz with no question left for the customer,
	// or no first-to-answer round open now.
	ErrNoQuestion = errors.New("there is no question to answer")
	// ErrQuestionChanged refuses an answer to a question other than the one
	// the game asks now.
	ErrQuestionChanged = errors.New("the question has changed")
	// ErrPrizeHeld refuses a promo game to a customer who already holds the
	// offer it gives: winning would give them nothing.
	ErrPrizeHeld = errors.New("the customer already holds this game's offer")
)

// GameConfig is one game as the operator configures it, with today's figures.
type GameConfig struct {
	Code             string `json:"code"`
	Enabled          bool   `json:"enabled"`
	PrizeKind        string `json:"prizeKind"`
	PrizeValue       int64  `json:"prizeValue"`
	PrizeTemplateID  string `json:"prizeTemplateId,omitempty"`
	PlaysPerDay      int    `json:"playsPerDay"`
	Parameter        int    `json:"parameter"`
	CostMinor        int64  `json:"costMinor"`
	TargetRTPPercent int    `json:"targetRtpPercent"`
	DailyBudgetMinor int64  `json:"dailyBudgetMinor"`
	DailyPrizeLimit  int    `json:"dailyPrizeLimit"`
	// Read-only: the win chance, the return of a paid game, and today's
	// plays, prizes, and payout.
	WinPercent     float64 `json:"winPercent"`
	RTPPercent     float64 `json:"rtpPercent"`
	PlaysToday     int64   `json:"playsToday"`
	WinsToday      int64   `json:"winsToday"`
	PaidTodayMinor int64   `json:"paidTodayMinor"`
}

// GameQuestion is a quiz question or a first-to-answer round.
type GameQuestion struct {
	ID         string     `json:"id"`
	Game       string     `json:"game"`
	QuestionRU string     `json:"questionRu"`
	QuestionEN string     `json:"questionEn"`
	Options    []Option   `json:"options"`
	Correct    *int       `json:"correctIndex,omitempty"`
	OpensAt    time.Time  `json:"opensAt"`
	ArchivedAt *time.Time `json:"archivedAt,omitempty"`
	// Answers and Winners are the panel's counts.
	Answers int64 `json:"answers"`
	Winners int64 `json:"winners"`
}

// GameState is one game on the customer's screen.
type GameState struct {
	Code        string `json:"code"`
	PrizeKind   string `json:"prizeKind"`
	PrizeValue  int64  `json:"prizeValue"`
	PrizeTitle  string `json:"prizeTitleRu,omitempty"`
	PrizeTitleE string `json:"prizeTitleEn,omitempty"`
	CostMinor   int64  `json:"costMinor"`
	Currency    string `json:"currency"`
	Parameter   int    `json:"parameter"`
	PlaysLeft   int    `json:"playsLeft"`
	// Closed is true when the game has paid out what it may today, or the
	// round's prizes are taken.
	Closed   bool          `json:"closed"`
	Question *GameQuestion `json:"question,omitempty"`
	Streak   int           `json:"streak,omitempty"`
}

// GamePlay is one play's result.
type GamePlay struct {
	ID         string    `json:"id"`
	Game       string    `json:"game"`
	Outcome    Outcome   `json:"outcome"`
	Won        bool      `json:"won"`
	PrizeKind  string    `json:"prizeKind,omitempty"`
	PrizeValue int64     `json:"prizeValue"`
	CostMinor  int64     `json:"costMinor"`
	Currency   string    `json:"currency"`
	CreatedAt  time.Time `json:"createdAt"`
}

// PlayInput is what the customer chose: a number for guess_number, an answer
// index and the question it answers for the quiz and first-to-answer.
type PlayInput struct {
	Choice     *int   `json:"choice"`
	QuestionID string `json:"questionId"`
}

func knownGame(code string) bool {
	for _, game := range Games {
		if game == code {
			return true
		}
	}
	return false
}

// questionGame reports a game that is a question rather than a draw.
func questionGame(code string) bool { return code == GameQuiz || code == GameFirstToAnswer }

const gameColumns = `code, enabled, prize_kind, prize_value, COALESCE(prize_template_id::text, ''), plays_per_day,
	parameter, cost_minor, target_rtp_percent, daily_budget_minor, daily_prize_limit`

func scanGame(row pgx.Row) (GameConfig, error) {
	var game GameConfig
	err := row.Scan(&game.Code, &game.Enabled, &game.PrizeKind, &game.PrizeValue, &game.PrizeTemplateID,
		&game.PlaysPerDay, &game.Parameter, &game.CostMinor, &game.TargetRTPPercent, &game.DailyBudgetMinor,
		&game.DailyPrizeLimit)
	return game, err
}

// prizeValueMinor values a prize for the budget: a day at the day value, a
// promo offer at nothing (its limit is the daily number of prizes).
func prizeValueMinor(game GameConfig, settings Settings) int64 {
	switch game.PrizeKind {
	case KindWallet:
		return game.PrizeValue
	case KindDays:
		return game.PrizeValue * settings.DayValueMinor
	default:
		return 0
	}
}

// today is the current UTC day as YYYY-MM-DD; queries cast it to date, so
// the session's time zone never moves a play to another day.
func (service *Service) today() string {
	return service.clock().UTC().Format(time.DateOnly)
}

// GameConfigs lists every game with today's figures, for the panel.
func (service *Service) GameConfigs(ctx context.Context) ([]GameConfig, error) {
	settings, err := service.Settings(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := service.pool.Query(ctx, `SELECT `+gameColumns+` FROM daily_games`)
	if err != nil {
		return nil, err
	}
	configs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (GameConfig, error) { return scanGame(row) })
	if err != nil {
		return nil, err
	}
	byCode := map[string]GameConfig{}
	for _, game := range configs {
		game.WinPercent = WinProbability(game.Code, game.Parameter) * 100
		game.RTPPercent = GameRTPPercent(game.Code, game.Parameter, prizeValueMinor(game, settings), game.CostMinor)
		if err = service.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE won),
				COALESCE(sum(prize_value_minor), 0)::bigint
			FROM game_plays WHERE game_code = $1 AND play_day = $2::date`, game.Code, service.today()).
			Scan(&game.PlaysToday, &game.WinsToday, &game.PaidTodayMinor); err != nil {
			return nil, err
		}
		byCode[game.Code] = game
	}
	ordered := make([]GameConfig, 0, len(Games))
	for _, code := range Games {
		if game, ok := byCode[code]; ok {
			ordered = append(ordered, game)
		}
	}
	return ordered, nil
}

// SaveGame stores one game's configuration. A paid game whose expected
// payout is above its target return is refused, as the wheel's is; a days
// prize needs the engagement days plan; a promo prize a live offer template.
func (service *Service) SaveGame(ctx context.Context, game GameConfig, actor panelpg.Actor) error {
	if !knownGame(game.Code) {
		return ErrNotFound
	}
	if game.PrizeKind != KindPromo {
		game.PrizeTemplateID = ""
	} else {
		game.PrizeValue = 0
	}
	switch game.Code {
	case GameQuiz, GameDailyStreak, GameFirstToAnswer:
		game.PlaysPerDay = 1
	}
	settings, err := service.Settings(ctx)
	if err != nil {
		return err
	}
	if !validGame(game) || (game.PrizeKind == KindDays && settings.DaysPlanVersionID == "") {
		return ErrInvalid
	}
	if game.CostMinor > 0 && GameRTPPercent(game.Code, game.Parameter, prizeValueMinor(game, settings), game.CostMinor) >
		float64(game.TargetRTPPercent) {
		return ErrRTP
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if game.PrizeKind == KindPromo {
		var live bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM offer_templates
			WHERE id = $1::uuid AND archived_at IS NULL)`, game.PrizeTemplateID).Scan(&live); err != nil || !live {
			return ErrInvalid
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE daily_games SET enabled = $2, prize_kind = $3, prize_value = $4,
			prize_template_id = NULLIF($5, '')::uuid, plays_per_day = $6, parameter = $7, cost_minor = $8,
			target_rtp_percent = $9, daily_budget_minor = $10, daily_prize_limit = $11,
			updated_at = now(), updated_by = NULLIF($12, '')::uuid
		WHERE code = $1`, game.Code, game.Enabled, game.PrizeKind, game.PrizeValue, game.PrizeTemplateID,
		game.PlaysPerDay, game.Parameter, game.CostMinor, game.TargetRTPPercent, game.DailyBudgetMinor,
		game.DailyPrizeLimit, actor.AdminID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23514" {
			return ErrInvalid
		}
		return err
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.engagement.game_saved", "marketing", "daily_game", game.Code,
		map[string]any{
			"enabled": game.Enabled, "prizeKind": game.PrizeKind, "prizeValue": game.PrizeValue,
			"costMinor": game.CostMinor, "dailyBudgetMinor": game.DailyBudgetMinor, "dailyPrizeLimit": game.DailyPrizeLimit,
		}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func validGame(game GameConfig) bool {
	prize := (game.PrizeKind == KindWallet && game.PrizeValue > 0) ||
		(game.PrizeKind == KindDays && game.PrizeValue >= 1 && game.PrizeValue <= 365) ||
		(game.PrizeKind == KindPromo && game.PrizeTemplateID != "")
	parameter := game.Parameter == 0
	switch game.Code {
	case GameDice:
		parameter = game.Parameter >= 1 && game.Parameter <= 6
	case GameGuessNumber:
		parameter = game.Parameter >= 2 && game.Parameter <= 10
	case GameDailyStreak:
		parameter = game.Parameter >= 2 && game.Parameter <= 30
	case GameFirstToAnswer:
		parameter = game.Parameter >= 1 && game.Parameter <= 100
	}
	return prize && parameter && game.PlaysPerDay >= 1 && game.PlaysPerDay <= 20 && game.CostMinor >= 0 &&
		game.TargetRTPPercent >= 1 && game.TargetRTPPercent <= 100 && game.DailyBudgetMinor >= 0 &&
		game.DailyPrizeLimit >= 0
}

// GameQuestions lists a question game's questions, newest first, with the
// answers and winners each drew. The correct answer is included: this is the
// operator's view.
func (service *Service) GameQuestions(ctx context.Context) ([]GameQuestion, error) {
	rows, err := service.pool.Query(ctx, `SELECT q.id::text, q.game_code, q.question_ru, q.question_en, q.options,
			q.correct_index, q.opens_at, q.archived_at,
			(SELECT count(*) FROM game_plays p WHERE p.question_id = q.id),
			(SELECT count(*) FROM game_plays p WHERE p.question_id = q.id AND p.won)
		FROM game_questions q ORDER BY q.opens_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (GameQuestion, error) {
		var (
			question GameQuestion
			options  []byte
			correct  int
		)
		err := row.Scan(&question.ID, &question.Game, &question.QuestionRU, &question.QuestionEN, &options, &correct,
			&question.OpensAt, &question.ArchivedAt, &question.Answers, &question.Winners)
		if err == nil {
			question.Correct = &correct
			err = json.Unmarshal(options, &question.Options)
		}
		return question, err
	})
}

// CreateGameQuestion adds a quiz question or opens a first-to-answer round.
func (service *Service) CreateGameQuestion(ctx context.Context, question GameQuestion, actor panelpg.Actor) (GameQuestion, error) {
	question.QuestionRU, question.QuestionEN = strings.TrimSpace(question.QuestionRU), strings.TrimSpace(question.QuestionEN)
	if question.OpensAt.IsZero() {
		question.OpensAt = service.clock().UTC()
	}
	valid := questionGame(question.Game) && question.QuestionRU != "" && question.QuestionEN != "" &&
		utf8.RuneCountInString(question.QuestionRU) <= 300 && utf8.RuneCountInString(question.QuestionEN) <= 300 &&
		len(question.Options) >= 2 && len(question.Options) <= 6 && question.Correct != nil &&
		*question.Correct >= 0 && *question.Correct < len(question.Options)
	for index, option := range question.Options {
		option.RU, option.EN = strings.TrimSpace(option.RU), strings.TrimSpace(option.EN)
		question.Options[index] = option
		valid = valid && option.RU != "" && option.EN != "" &&
			utf8.RuneCountInString(option.RU) <= 60 && utf8.RuneCountInString(option.EN) <= 60
	}
	if !valid {
		return GameQuestion{}, ErrInvalid
	}
	options, err := json.Marshal(question.Options)
	if err != nil {
		return GameQuestion{}, err
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return GameQuestion{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = tx.QueryRow(ctx, `INSERT INTO game_questions (game_code, question_ru, question_en, options, correct_index,
			opens_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')::uuid) RETURNING id::text`, question.Game, question.QuestionRU,
		question.QuestionEN, options, *question.Correct, question.OpensAt, actor.AdminID).Scan(&question.ID); err != nil {
		return GameQuestion{}, err
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.engagement.game_question_created", "marketing", "game_question",
		question.ID, map[string]any{"game": question.Game, "opensAt": question.OpensAt}); err != nil {
		return GameQuestion{}, err
	}
	return question, tx.Commit(ctx)
}

// ArchiveGameQuestion withdraws a question; plays that answered it stay.
func (service *Service) ArchiveGameQuestion(ctx context.Context, questionID string, actor panelpg.Actor) error {
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE game_questions SET archived_at = now() WHERE id = $1::uuid AND archived_at IS NULL`, questionID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return ErrNotFound
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.engagement.game_question_archived", "marketing", "game_question",
		questionID, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// currentQuestion is the question the game asks this customer now: for the
// quiz, the oldest open question they have not answered; for first-to-answer,
// the newest round opened within the last day.
func (service *Service) currentQuestion(ctx context.Context, db rower, customerID, code string) (*GameQuestion, error) {
	query := `SELECT q.id::text, q.game_code, q.question_ru, q.question_en, q.options, q.correct_index, q.opens_at,
			(SELECT count(*) FROM game_plays p WHERE p.question_id = q.id AND p.won)
		FROM game_questions q
		WHERE q.game_code = 'quiz' AND q.archived_at IS NULL AND q.opens_at <= now()
		  AND NOT EXISTS (SELECT 1 FROM game_plays p WHERE p.question_id = q.id AND p.user_id = $1::uuid)
		ORDER BY q.opens_at, q.id LIMIT 1`
	if code == GameFirstToAnswer {
		query = `SELECT q.id::text, q.game_code, q.question_ru, q.question_en, q.options, q.correct_index, q.opens_at,
				(SELECT count(*) FROM game_plays p WHERE p.question_id = q.id AND p.won)
			FROM game_questions q
			WHERE q.game_code = 'first_to_answer' AND q.archived_at IS NULL AND q.opens_at <= now()
			  AND q.opens_at > now() - interval '1 day' AND $1::uuid IS NOT NULL
			ORDER BY q.opens_at DESC, q.id LIMIT 1`
	}
	var (
		question GameQuestion
		options  []byte
		correct  int
	)
	err := db.QueryRow(ctx, query, customerID).Scan(&question.ID, &question.Game, &question.QuestionRU,
		&question.QuestionEN, &options, &correct, &question.OpensAt, &question.Winners)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	question.Correct = &correct
	return &question, json.Unmarshal(options, &question.Options)
}

// streakBefore counts the customer's consecutive daily check-ins ending the
// day before today.
func (service *Service) streakBefore(ctx context.Context, db rower, customerID, today string) (int, error) {
	var streak int
	err := db.QueryRow(ctx, `WITH days AS (
			SELECT DISTINCT play_day FROM game_plays
			WHERE user_id = $1::uuid AND game_code = 'daily_streak'
			  AND play_day < $2::date AND play_day >= $2::date - 400
		), numbered AS (
			SELECT play_day, ($2::date - play_day) AS distance, row_number() OVER (ORDER BY play_day DESC) AS position
			FROM days
		)
		SELECT count(*) FROM numbered WHERE distance = position`, customerID, today).Scan(&streak)
	return streak, err
}

type gameDay struct {
	plays, wins, paid int64
}

func (service *Service) gameToday(ctx context.Context, db rower, customerID, code, today string) (gameDay, int64, error) {
	var day gameDay
	var mine int64
	err := db.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE won), COALESCE(sum(prize_value_minor), 0)::bigint,
			count(*) FILTER (WHERE user_id = $1::uuid)
		FROM game_plays WHERE game_code = $2 AND play_day = $3::date`, customerID, code, today).
		Scan(&day.plays, &day.wins, &day.paid, &mine)
	return day, mine, err
}

// closedToday reports a game that cannot pay its prize again today.
func closedToday(game GameConfig, day gameDay, value int64) bool {
	return (game.DailyPrizeLimit > 0 && day.wins >= int64(game.DailyPrizeLimit)) ||
		(game.DailyBudgetMinor > 0 && day.paid+value > game.DailyBudgetMinor)
}

// CustomerGames lists the enabled games as the customer sees them, with the
// customer's last plays.
func (service *Service) CustomerGames(ctx context.Context, customerID string) ([]GameState, []GamePlay, error) {
	settings, err := service.Settings(ctx)
	if err != nil {
		return nil, nil, err
	}
	rows, err := service.pool.Query(ctx, `SELECT `+gameColumns+` FROM daily_games WHERE enabled`)
	if err != nil {
		return nil, nil, err
	}
	configs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (GameConfig, error) { return scanGame(row) })
	if err != nil {
		return nil, nil, err
	}
	byCode := map[string]GameConfig{}
	for _, game := range configs {
		byCode[game.Code] = game
	}
	today := service.today()
	states := []GameState{}
	for _, code := range Games {
		game, ok := byCode[code]
		if !ok {
			continue
		}
		state := GameState{Code: code, PrizeKind: game.PrizeKind, PrizeValue: game.PrizeValue, CostMinor: game.CostMinor,
			Currency: settings.Currency, Parameter: game.Parameter}
		if game.PrizeKind == KindPromo {
			_ = service.pool.QueryRow(ctx, `SELECT title_ru, title_en FROM offer_templates WHERE id = $1::uuid`,
				game.PrizeTemplateID).Scan(&state.PrizeTitle, &state.PrizeTitleE)
		}
		day, mine, dayErr := service.gameToday(ctx, service.pool, customerID, code, today)
		if dayErr != nil {
			return nil, nil, dayErr
		}
		state.PlaysLeft = max(game.PlaysPerDay-int(mine), 0)
		state.Closed = closedToday(game, day, prizeValueMinor(game, settings))
		if questionGame(code) {
			question, questionErr := service.currentQuestion(ctx, service.pool, customerID, code)
			if questionErr != nil {
				return nil, nil, questionErr
			}
			if question != nil {
				answered := false
				if code == GameFirstToAnswer {
					state.PlaysLeft = 1
					if err = service.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM game_plays
						WHERE question_id = $1::uuid AND user_id = $2::uuid)`, question.ID, customerID).Scan(&answered); err != nil {
						return nil, nil, err
					}
					if answered {
						state.PlaysLeft = 0
					}
					state.Closed = state.Closed || question.Winners >= int64(game.Parameter)
				}
				// The customer never sees the correct answer or how others did.
				question.Correct, question.Winners = nil, 0
				state.Question = question
			} else {
				state.PlaysLeft = 0
			}
		}
		if code == GameDailyStreak {
			streak, streakErr := service.streakBefore(ctx, service.pool, customerID, today)
			if streakErr != nil {
				return nil, nil, streakErr
			}
			if mine > 0 {
				streak++
			}
			state.Streak = streak
		}
		states = append(states, state)
	}
	recentRows, err := service.pool.Query(ctx, `SELECT id::text, game_code, outcome, won, COALESCE(prize_kind, ''),
			prize_value, cost_minor, currency, created_at
		FROM game_plays WHERE user_id = $1::uuid ORDER BY created_at DESC LIMIT 10`, customerID)
	if err != nil {
		return nil, nil, err
	}
	recent, err := pgx.CollectRows(recentRows, func(row pgx.CollectableRow) (GamePlay, error) { return scanPlay(row) })
	return states, recent, err
}

func scanPlay(row pgx.Row) (GamePlay, error) {
	var (
		play    GamePlay
		outcome []byte
	)
	err := row.Scan(&play.ID, &play.Game, &outcome, &play.Won, &play.PrizeKind, &play.PrizeValue, &play.CostMinor,
		&play.Currency, &play.CreatedAt)
	if err == nil {
		err = json.Unmarshal(outcome, &play.Outcome)
	}
	return play, err
}

// PlayGame plays one game once. The fee, the draw or the answer, the play
// row, and the prize commit together under serializable isolation, so two
// plays racing for the last prize of the day cannot both win it; a replay
// with the same key returns the play already made, and the prize is keyed
// on the play, so nothing is paid twice.
func (service *Service) PlayGame(ctx context.Context, customerID, code string, input PlayInput, key string) (GamePlay, error) {
	key = strings.TrimSpace(key)
	if len(key) < 8 || len(key) > 128 {
		return GamePlay{}, ErrInvalid
	}
	if !knownGame(code) {
		return GamePlay{}, ErrGameDisabled
	}
	var play GamePlay
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		play, err = service.playGame(ctx, customerID, code, input, key)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01") {
			continue
		}
		break
	}
	return play, err
}

func (service *Service) playGame(ctx context.Context, customerID, code string, input PlayInput, key string) (GamePlay, error) {
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return GamePlay{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	existing, err := scanPlay(tx.QueryRow(ctx, `SELECT id::text, game_code, outcome, won, COALESCE(prize_kind, ''),
			prize_value, cost_minor, currency, created_at
		FROM game_plays WHERE user_id = $1::uuid AND idempotency_key = $2`, customerID, key))
	if err == nil {
		return existing, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return GamePlay{}, err
	}
	game, err := scanGame(tx.QueryRow(ctx, `SELECT `+gameColumns+` FROM daily_games WHERE code = $1 AND enabled`, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return GamePlay{}, ErrGameDisabled
	}
	if err != nil {
		return GamePlay{}, err
	}
	settings, err := readSettings(ctx, tx)
	if err != nil {
		return GamePlay{}, err
	}
	if err = service.oldEnough(ctx, tx, customerID, settings); err != nil {
		return GamePlay{}, err
	}
	today := service.today()
	day, mine, err := service.gameToday(ctx, tx, customerID, code, today)
	if err != nil {
		return GamePlay{}, err
	}
	if code != GameFirstToAnswer && mine >= int64(game.PlaysPerDay) {
		return GamePlay{}, ErrLimit
	}
	value := prizeValueMinor(game, settings)
	if closedToday(game, day, value) {
		return GamePlay{}, ErrGameClosed
	}
	if game.PrizeKind == KindPromo {
		var held bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM personal_offers o JOIN offer_templates t ON t.id = $2::uuid
			WHERE o.user_id = $1::uuid AND o.promotion_id = t.promotion_id AND o.status = 'active')`,
			customerID, game.PrizeTemplateID).Scan(&held); err != nil {
			return GamePlay{}, err
		}
		if held {
			return GamePlay{}, ErrPrizeHeld
		}
	}

	var (
		outcome    Outcome
		questionID *string
	)
	switch code {
	case GameDice:
		outcome, err = PlayDice(game.Parameter)
	case GameSlots:
		outcome, err = PlaySlots()
	case GameLuckyTicket:
		outcome, err = PlayLuckyTicket()
	case GameGuessNumber:
		if input.Choice == nil {
			return GamePlay{}, ErrInvalid
		}
		outcome, err = PlayGuessNumber(game.Parameter, *input.Choice)
	case GameDailyStreak:
		streak, streakErr := service.streakBefore(ctx, tx, customerID, today)
		if streakErr != nil {
			return GamePlay{}, streakErr
		}
		outcome = CheckIn(streak+1, game.Parameter)
	case GameQuiz, GameFirstToAnswer:
		question, questionErr := service.currentQuestion(ctx, tx, customerID, code)
		if questionErr != nil {
			return GamePlay{}, questionErr
		}
		if question == nil {
			return GamePlay{}, ErrNoQuestion
		}
		if question.ID != input.QuestionID {
			return GamePlay{}, ErrQuestionChanged
		}
		if code == GameFirstToAnswer && question.Winners >= int64(game.Parameter) {
			return GamePlay{}, ErrGameClosed
		}
		if input.Choice == nil {
			return GamePlay{}, ErrInvalid
		}
		outcome, err = AnswerQuestion(len(question.Options), *question.Correct, *input.Choice)
		questionID = &question.ID
	}
	if errors.Is(err, ErrBadChoice) {
		return GamePlay{}, ErrInvalid
	}
	if err != nil {
		return GamePlay{}, err
	}

	play := GamePlay{Game: code, Outcome: outcome, Won: outcome.Won, CostMinor: game.CostMinor, Currency: settings.Currency}
	var prizeKind *string
	prizeMinor := int64(0)
	if outcome.Won {
		play.PrizeKind, play.PrizeValue, prizeMinor = game.PrizeKind, game.PrizeValue, value
		prizeKind = &game.PrizeKind
	}
	encoded, err := json.Marshal(outcome)
	if err != nil {
		return GamePlay{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO game_plays (user_id, game_code, play_day, question_id, choice, outcome, won,
			prize_kind, prize_value, prize_value_minor, cost_minor, currency, idempotency_key)
		VALUES ($1::uuid, $2, $3::date, $4::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id::text, created_at`,
		customerID, code, today, questionID, input.Choice, encoded, outcome.Won, prizeKind, play.PrizeValue, prizeMinor,
		game.CostMinor, settings.Currency, key).Scan(&play.ID, &play.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "game_plays_one_answer" {
			return GamePlay{}, ErrAnswered
		}
		return GamePlay{}, err
	}
	if game.CostMinor > 0 {
		if _, err = walletpost.Post(ctx, tx, walletpost.Posting{
			Type: "debit", ReferenceType: "game_play", ReferenceID: play.ID, IdempotencyKey: "game-play:" + play.ID,
			Reason: "Daily game entry", ActorID: customerID, CustomerID: customerID, Currency: settings.Currency,
			AmountMinor: -game.CostMinor, CheckBalance: true,
		}); err != nil {
			if errors.Is(err, walletpost.ErrInsufficient) {
				return GamePlay{}, ErrInsufficient
			}
			return GamePlay{}, err
		}
	}
	if outcome.Won {
		switch game.PrizeKind {
		case KindPromo:
			var offerID string
			if err = tx.QueryRow(ctx, `INSERT INTO personal_offers (user_id, promotion_id, plan_id, title_ru, title_en,
					terms_ru, terms_en, starts_at, expires_at, template_id, activation_hours, bonus_minor, bonus_currency)
				SELECT $1::uuid, t.promotion_id, t.plan_id, t.title_ru, t.title_en, t.terms_ru, t.terms_en,
					now(), now() + make_interval(hours => t.valid_hours), t.id, t.activation_hours, t.bonus_minor, t.bonus_currency
				FROM offer_templates t WHERE t.id = $2::uuid
				RETURNING id::text`, customerID, game.PrizeTemplateID).Scan(&offerID); err != nil {
				return GamePlay{}, err
			}
			if _, err = tx.Exec(ctx, `UPDATE game_plays SET personal_offer_id = $2::uuid WHERE id = $1::uuid`,
				play.ID, offerID); err != nil {
				return GamePlay{}, err
			}
		default:
			if err = service.pay(ctx, tx, customerID, settings, game.PrizeKind, game.PrizeValue,
				"game-prize:"+play.ID, "game_prize", play.ID); err != nil {
				return GamePlay{}, err
			}
		}
	}
	return play, tx.Commit(ctx)
}
