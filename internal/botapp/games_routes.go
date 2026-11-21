package botapp

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/engagement"
)

// gameActions is the closed set of callback actions the daily games own. It
// is folded into commerceActions, which is what lets a tap reach the handler
// below at all. A play carries the plays the customer had left when the
// button was drawn, so a second deliberate tap on a redrawn screen is a new
// play while a double tap of the same button stays one.
var gameActions = map[string]bool{"games": true, "game": true, "game-play": true, "game-guess": true, "game-ans": true}

// gameShortCodes keeps an answer's callback data under Telegram's 64 bytes:
// a question's UUID leaves no room for the game's full name.
var gameShortCodes = map[string]string{"q": engagement.GameQuiz, "f": engagement.GameFirstToAnswer}

// slotSymbols draws the slot reels.
var slotSymbols = []string{"🍒", "🍋", "🔔", "⭐", "🍀", "7️⃣"}

func (app *App) handleGameAction(ctx context.Context, session commerceContext, parts []string) (View, bool) {
	if app.games == nil {
		return View{}, false
	}
	switch parts[0] {
	case "games":
		return app.gamesScreen(ctx, session), true
	case "game":
		return app.gameScreen(ctx, session, argumentAt(parts, 1), ""), true
	case "game-play":
		return app.playGame(ctx, session, argumentAt(parts, 1), engagement.PlayInput{}), true
	case "game-guess":
		guess, err := strconv.Atoi(argumentAt(parts, 1))
		if err != nil {
			return app.gamesScreen(ctx, session), true
		}
		return app.playGame(ctx, session, engagement.GameGuessNumber, engagement.PlayInput{Choice: &guess}), true
	case "game-ans":
		code := gameShortCodes[argumentAt(parts, 1)]
		choice, err := strconv.Atoi(argumentAt(parts, 2))
		questionID, expanded := expandCompactID(argumentAt(parts, 3))
		if code == "" || err != nil || !expanded {
			return app.gamesScreen(ctx, session), true
		}
		return app.playGame(ctx, session, code, engagement.PlayInput{Choice: &choice, QuestionID: questionID}), true
	default:
		return View{}, false
	}
}

// gamesEnabled reports whether any daily game is switched on, which is when
// the invitation screen offers them.
func (app *App) gamesEnabled(ctx context.Context) bool {
	if app.games == nil || app.customers == nil {
		return false
	}
	var enabled bool
	if err := app.customers.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM daily_games WHERE enabled)`).Scan(&enabled); err != nil {
		return false
	}
	return enabled
}

func (app *App) loadGames(ctx context.Context, session commerceContext) ([]engagement.GameState, bool) {
	states, _, err := app.games.CustomerGames(ctx, session.Customer.ID)
	if err != nil {
		app.logError(ctx, "daily games lookup failed", "error", err)
		return nil, false
	}
	return states, true
}

// gamesScreen lists the games with their prizes and the plays left today.
func (app *App) gamesScreen(ctx context.Context, session commerceContext) View {
	locale := session.Locale
	states, ok := app.loadGames(ctx, session)
	if !ok {
		return app.errorView(locale, routeReferral)
	}
	back := row(callbackButton(text(locale, "action.back"), routeReferral))
	if len(states) == 0 {
		return View{Text: text(locale, "games.none"), Keyboard: keyboard(back)}
	}
	lines := []string{text(locale, "games.title"), ""}
	rows := make([][]models.InlineKeyboardButton, 0, len(states)+1)
	for _, state := range states {
		status := text(locale, "games.left", state.PlaysLeft)
		if state.Closed {
			status = text(locale, "games.closed")
		}
		lines = append(lines, fmt.Sprintf("%s — %s · %s", text(locale, "games.name."+state.Code), gamePrize(locale, state), status))
		rows = append(rows, row(actionButton(text(locale, "games.name."+state.Code), "game:"+state.Code)))
	}
	rows = append(rows, back)
	return View{Text: strings.Join(lines, "\n"), Keyboard: keyboard(rows...)}
}

// gameScreen shows one game: its rules, the prize, and what can be tapped.
func (app *App) gameScreen(ctx context.Context, session commerceContext, code, result string) View {
	locale := session.Locale
	states, ok := app.loadGames(ctx, session)
	if !ok {
		return app.errorView(locale, routeReferral)
	}
	var state *engagement.GameState
	for index := range states {
		if states[index].Code == code {
			state = &states[index]
		}
	}
	if state == nil {
		return app.gamesScreen(ctx, session)
	}
	lines := []string{}
	if result != "" {
		lines = append(lines, result, "")
	}
	rules := text(locale, "games.rules."+code)
	switch code {
	case engagement.GameDice, engagement.GameGuessNumber, engagement.GameDailyStreak, engagement.GameFirstToAnswer:
		rules = text(locale, "games.rules."+code, state.Parameter)
	}
	lines = append(lines, "<b>"+text(locale, "games.name."+code)+"</b>", rules,
		text(locale, "games.prize", gamePrize(locale, *state)))
	if state.CostMinor > 0 {
		lines = append(lines, text(locale, "games.cost", formatMoney(state.CostMinor, state.Currency)))
	}
	if code == engagement.GameDailyStreak {
		lines = append(lines, text(locale, "games.streak", state.Streak))
	}
	rows := [][]models.InlineKeyboardButton{}
	playsLeft, gameCode := state.PlaysLeft, state.Code
	playable := state.PlaysLeft > 0 && !state.Closed
	switch {
	case state.Closed:
		lines = append(lines, "", text(locale, "games.closed"))
	case state.Question == nil && (code == engagement.GameQuiz || code == engagement.GameFirstToAnswer):
		lines = append(lines, "", text(locale, "games.noQuestion"))
	case !playable:
		lines = append(lines, "", text(locale, "games.doneToday"))
	}
	if state.Question != nil && !state.Closed {
		question := state.Question.QuestionEN
		if locale == LocaleRussian {
			question = state.Question.QuestionRU
		}
		lines = append(lines, "", "❓ "+html.EscapeString(question))
		if playable {
			for index, option := range state.Question.Options {
				label := option.EN
				if locale == LocaleRussian {
					label = option.RU
				}
				action := fmt.Sprintf("game-ans:q:%d:%s", index, compactID(state.Question.ID))
				if code == engagement.GameFirstToAnswer {
					action = fmt.Sprintf("game-ans:f:%d:%s", index, compactID(state.Question.ID))
				}
				rows = append(rows, row(actionButton(truncateRunes(label, 40), action)))
			}
		}
	}
	if playable {
		switch code {
		case engagement.GameGuessNumber:
			numbers := []models.InlineKeyboardButton{}
			for number := 1; number <= state.Parameter; number++ {
				numbers = append(numbers, actionButton(strconv.Itoa(number), fmt.Sprintf("game-guess:%d:%d", number, playsLeft)))
				if len(numbers) == 5 {
					rows = append(rows, numbers)
					numbers = []models.InlineKeyboardButton{}
				}
			}
			if len(numbers) > 0 {
				rows = append(rows, numbers)
			}
		case engagement.GameDice, engagement.GameSlots, engagement.GameLuckyTicket, engagement.GameDailyStreak:
			rows = append(rows, row(actionButton(text(locale, "games.play."+code), fmt.Sprintf("game-play:%s:%d", gameCode, playsLeft))))
		}
	}
	rows = append(rows, row(actionButton(text(locale, "action.back"), "games"), callbackButton(text(locale, "action.menu"), routeHome)))
	return View{Text: strings.Join(lines, "\n"), Keyboard: keyboard(rows...)}
}

// playGame plays once and shows the result above the game's screen.
func (app *App) playGame(ctx context.Context, session commerceContext, code string, input engagement.PlayInput) View {
	locale := session.Locale
	play, err := app.games.PlayGame(ctx, session.Customer.ID, code, input, session.TapKey)
	switch {
	case err == nil:
		return app.gameScreen(ctx, session, code, gameResult(locale, play))
	case errors.Is(err, engagement.ErrGameDisabled):
		return app.gamesScreen(ctx, session)
	case errors.Is(err, engagement.ErrInsufficient):
		return app.gameScreen(ctx, session, code, text(locale, "games.insufficient"))
	case errors.Is(err, engagement.ErrTooNew):
		return app.gameScreen(ctx, session, code, text(locale, "games.tooNew"))
	case errors.Is(err, engagement.ErrPrizeHeld):
		return app.gameScreen(ctx, session, code, text(locale, "games.prizeHeld"))
	case errors.Is(err, engagement.ErrLimit), errors.Is(err, engagement.ErrGameClosed), errors.Is(err, engagement.ErrAnswered),
		errors.Is(err, engagement.ErrQuestionChanged), errors.Is(err, engagement.ErrNoQuestion), errors.Is(err, engagement.ErrInvalid):
		return app.gameScreen(ctx, session, code, "")
	default:
		app.logError(ctx, "daily game play failed", "error", err, "game", code)
		return app.errorView(locale, routeReferral)
	}
}

// gameResult describes what a play drew and whether it won.
func gameResult(locale Locale, play engagement.GamePlay) string {
	outcome := play.Outcome
	drawn := ""
	switch play.Game {
	case engagement.GameDice:
		drawn = text(locale, "games.result.dice", outcome.Roll)
	case engagement.GameSlots:
		symbols := make([]string, 0, len(outcome.Reels))
		for _, reel := range outcome.Reels {
			if reel >= 0 && reel < len(slotSymbols) {
				symbols = append(symbols, slotSymbols[reel])
			}
		}
		drawn = strings.Join(symbols, " ")
	case engagement.GameGuessNumber:
		drawn = text(locale, "games.result.guess", outcome.Guess, outcome.Secret)
	case engagement.GameLuckyTicket:
		drawn = text(locale, "games.result.ticket", outcome.Ticket)
	case engagement.GameDailyStreak:
		drawn = text(locale, "games.result.streak", outcome.Streak)
	case engagement.GameQuiz, engagement.GameFirstToAnswer:
		if outcome.Won {
			drawn = text(locale, "games.result.right")
		} else {
			drawn = text(locale, "games.result.wrong")
		}
	}
	verdict := text(locale, "games.lost")
	if play.Won {
		verdict = text(locale, "games.won")
	}
	return drawn + "\n" + verdict
}

// gamePrize names a game's prize in the customer's language.
func gamePrize(locale Locale, state engagement.GameState) string {
	switch state.PrizeKind {
	case engagement.KindWallet:
		return formatMoney(state.PrizeValue, state.Currency)
	case engagement.KindDays:
		return text(locale, "games.days", state.PrizeValue)
	default:
		title := state.PrizeTitleE
		if locale == LocaleRussian {
			title = state.PrizeTitle
		}
		return html.EscapeString(title)
	}
}
