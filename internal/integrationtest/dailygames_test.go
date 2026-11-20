//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/omniflow/omniflow/internal/engagement"
	"github.com/omniflow/omniflow/internal/panelpg"
)

func diceGame(prize int64) engagement.GameConfig {
	return engagement.GameConfig{Code: engagement.GameDice, Enabled: true, PrizeKind: engagement.KindWallet,
		PrizeValue: prize, PlaysPerDay: 2, Parameter: 1, TargetRTPPercent: 80}
}

// A die that always wins pays every play once through the ledger, keeps a
// replay to the same play, stops at the customer's daily limit, closes when
// the day's prizes are spent, refuses an entry fee above the target return,
// and refuses an account too new.
func TestDailyGamesPayOnceWithinLimits(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	service := engagement.New(harness.pool, store)
	actor := harness.operator(ctx, t, "games@omniflow.test")

	paid := diceGame(500)
	paid.CostMinor = 400
	if err := service.SaveGame(ctx, paid, actor); !errors.Is(err, engagement.ErrRTP) {
		t.Fatalf("a 125%% return: %v", err)
	}
	game := diceGame(500)
	game.DailyPrizeLimit = 3
	if err := service.SaveGame(ctx, game, actor); err != nil {
		t.Fatal(err)
	}

	if _, err := service.PlayGame(ctx, harness.customer(ctx, t), engagement.GameDice, engagement.PlayInput{}, "dice-young-1"); !errors.Is(err, engagement.ErrTooNew) {
		t.Fatalf("a new account played: %v", err)
	}
	player := harness.agedCustomer(ctx, t)
	first, err := service.PlayGame(ctx, player, engagement.GameDice, engagement.PlayInput{}, "dice-key-0001")
	if err != nil || !first.Won || first.Outcome.Roll < 1 || first.Outcome.Roll > 6 {
		t.Fatalf("first play = %+v (%v)", first, err)
	}
	replay, err := service.PlayGame(ctx, player, engagement.GameDice, engagement.PlayInput{}, "dice-key-0001")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v (%v)", replay, err)
	}
	if _, err = service.PlayGame(ctx, player, engagement.GameDice, engagement.PlayInput{}, "dice-key-0002"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.PlayGame(ctx, player, engagement.GameDice, engagement.PlayInput{}, "dice-key-0003"); !errors.Is(err, engagement.ErrLimit) {
		t.Fatalf("over the daily limit: %v", err)
	}
	if balance := harness.walletBalance(ctx, t, player); balance != 1000 {
		t.Fatalf("balance = %d, want two prizes once each", balance)
	}

	// The third prize of the day goes to whoever commits first; nobody gets
	// a fourth, however many play at once.
	var wins sync.WaitGroup
	results := make(chan error, 3)
	for index := range 3 {
		customer := harness.agedCustomer(ctx, t)
		wins.Add(1)
		go func() {
			defer wins.Done()
			_, playErr := service.PlayGame(ctx, customer, engagement.GameDice, engagement.PlayInput{},
				"dice-race-000"+string(rune('a'+index)))
			results <- playErr
		}()
	}
	wins.Wait()
	close(results)
	won, closed := 0, 0
	for playErr := range results {
		switch {
		case playErr == nil:
			won++
		case errors.Is(playErr, engagement.ErrGameClosed):
			closed++
		default:
			t.Fatalf("a racing play: %v", playErr)
		}
	}
	if won != 1 || closed != 2 {
		t.Fatalf("race: %d won, %d closed; want 1 and 2", won, closed)
	}
	var prizes int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM ledger_transactions WHERE reference_type = 'game_prize'`).
		Scan(&prizes); err != nil || prizes != 3 {
		t.Fatalf("prize postings = %d (%v), want 3", prizes, err)
	}
	configs, err := service.GameConfigs(ctx)
	if err != nil || len(configs) != 7 || configs[0].WinsToday != 3 || configs[0].PaidTodayMinor != 1500 {
		t.Fatalf("panel figures = %+v (%v)", configs, err)
	}
}

// The quiz asks a question the customer has not answered and refuses an
// answer to another; first-to-answer pays only the first right answers; a
// guess outside the range is refused; a streak pays on its N-th day; a promo
// prize sends the template's offer and is refused while the customer holds it.
func TestDailyGameKinds(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	service := engagement.New(harness.pool, store)
	panel := newOperations(t, harness)
	actor := harness.operator(ctx, t, "game-kinds@omniflow.test")
	wallet := func(code string, parameter int) engagement.GameConfig {
		return engagement.GameConfig{Code: code, Enabled: true, PrizeKind: engagement.KindWallet, PrizeValue: 300,
			PlaysPerDay: 1, Parameter: parameter, TargetRTPPercent: 80}
	}
	for _, game := range []engagement.GameConfig{
		wallet(engagement.GameQuiz, 0), wallet(engagement.GameFirstToAnswer, 1),
		wallet(engagement.GameGuessNumber, 10), wallet(engagement.GameDailyStreak, 2),
	} {
		if err := service.SaveGame(ctx, game, actor); err != nil {
			t.Fatalf("save %s: %v", game.Code, err)
		}
	}
	correct := 1
	options := []engagement.Option{{RU: "Нет", EN: "No"}, {RU: "Да", EN: "Yes"}}
	quiz, err := service.CreateGameQuestion(ctx, engagement.GameQuestion{Game: engagement.GameQuiz,
		QuestionRU: "Шифрует ли VPN трафик?", QuestionEN: "Does a VPN encrypt traffic?", Options: options, Correct: &correct}, actor)
	if err != nil {
		t.Fatal(err)
	}
	round, err := service.CreateGameQuestion(ctx, engagement.GameQuestion{Game: engagement.GameFirstToAnswer,
		QuestionRU: "Два плюс два четыре?", QuestionEN: "Is two plus two four?", Options: options, Correct: &correct}, actor)
	if err != nil {
		t.Fatal(err)
	}

	player := harness.agedCustomer(ctx, t)
	states, _, err := service.CustomerGames(ctx, player)
	if err != nil || len(states) != 4 {
		t.Fatalf("states = %+v (%v)", states, err)
	}
	for _, state := range states {
		if state.Question != nil && state.Question.Correct != nil {
			t.Fatal("the customer was shown the right answer")
		}
	}
	answer := 1
	if _, err = service.PlayGame(ctx, player, engagement.GameQuiz,
		engagement.PlayInput{Choice: &answer, QuestionID: round.ID}, "quiz-wrong-question"); !errors.Is(err, engagement.ErrQuestionChanged) {
		t.Fatalf("an answer to another question: %v", err)
	}
	play, err := service.PlayGame(ctx, player, engagement.GameQuiz, engagement.PlayInput{Choice: &answer, QuestionID: quiz.ID}, "quiz-answer-1")
	if err != nil || !play.Won {
		t.Fatalf("quiz = %+v (%v)", play, err)
	}

	if _, err = service.PlayGame(ctx, player, engagement.GameFirstToAnswer,
		engagement.PlayInput{Choice: &answer, QuestionID: round.ID}, "first-answer-1"); err != nil {
		t.Fatal(err)
	}
	late := harness.agedCustomer(ctx, t)
	if _, err = service.PlayGame(ctx, late, engagement.GameFirstToAnswer,
		engagement.PlayInput{Choice: &answer, QuestionID: round.ID}, "first-answer-2"); !errors.Is(err, engagement.ErrGameClosed) {
		t.Fatalf("a second winner of a one-winner round: %v", err)
	}

	tooHigh := 11
	if _, err = service.PlayGame(ctx, player, engagement.GameGuessNumber, engagement.PlayInput{Choice: &tooHigh}, "guess-high-1"); !errors.Is(err, engagement.ErrInvalid) {
		t.Fatalf("a guess of 11 out of 10: %v", err)
	}
	guess := 3
	guessed, err := service.PlayGame(ctx, player, engagement.GameGuessNumber, engagement.PlayInput{Choice: &guess}, "guess-key-1")
	if err != nil || guessed.Outcome.Secret < 1 || guessed.Outcome.Secret > 10 || guessed.Won != (guessed.Outcome.Secret == 3) {
		t.Fatalf("guess = %+v (%v)", guessed, err)
	}

	if _, err = harness.pool.Exec(ctx, `INSERT INTO game_plays (user_id, game_code, play_day, won, currency, idempotency_key)
		VALUES ($1::uuid, 'daily_streak', (now() AT TIME ZONE 'UTC')::date - 1, false, 'RUB', 'yesterday-check-in')`, player); err != nil {
		t.Fatal(err)
	}
	streak, err := service.PlayGame(ctx, player, engagement.GameDailyStreak, engagement.PlayInput{}, "streak-today-1")
	if err != nil || streak.Outcome.Streak != 2 || !streak.Won {
		t.Fatalf("streak = %+v (%v)", streak, err)
	}
	want := int64(300 * 3) // the quiz, the round, and the streak
	if guessed.Won {
		want += 300
	}
	if balance := harness.walletBalance(ctx, t, player); balance != want {
		t.Fatalf("balance = %d, want %d", balance, want)
	}

	// A promo prize sends the template's personal offer.
	versionID := harness.catalog(ctx, t, "game-offer-plan", 10000)
	harness.promotion(ctx, t, "GAMETEN", harness.planIDOf(ctx, t, versionID), 1)
	var promotionID string
	if err = harness.pool.QueryRow(ctx, `SELECT id::text FROM promotions WHERE code = 'gameten'`).Scan(&promotionID); err != nil {
		t.Fatal(err)
	}
	template, err := panel.SaveOfferTemplate(ctx, panelpg.OfferTemplate{Code: "game-prize", PromotionID: promotionID,
		TitleRU: "Скидка за игру", TitleEN: "Game discount", ValidHours: 72}, actor)
	if err != nil {
		t.Fatal(err)
	}
	promo := engagement.GameConfig{Code: engagement.GameDice, Enabled: true, PrizeKind: engagement.KindPromo,
		PrizeTemplateID: template.ID, PlaysPerDay: 3, Parameter: 1, TargetRTPPercent: 80}
	if err = service.SaveGame(ctx, promo, actor); err != nil {
		t.Fatal(err)
	}
	if won, err := service.PlayGame(ctx, player, engagement.GameDice, engagement.PlayInput{}, "promo-dice-1"); err != nil || !won.Won || won.PrizeKind != "promo" {
		t.Fatalf("promo play = %+v (%v)", won, err)
	}
	var offers int
	if err = harness.pool.QueryRow(ctx, `SELECT count(*) FROM personal_offers WHERE user_id = $1::uuid AND template_id = $2::uuid
		AND status = 'active'`, player, template.ID).Scan(&offers); err != nil || offers != 1 {
		t.Fatalf("offers = %d (%v)", offers, err)
	}
	if _, err = service.PlayGame(ctx, player, engagement.GameDice, engagement.PlayInput{}, "promo-dice-2"); !errors.Is(err, engagement.ErrPrizeHeld) {
		t.Fatalf("a second offer while one is held: %v", err)
	}
}
