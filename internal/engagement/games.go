package engagement

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
)

// Daily game codes, Bedolaga's seven contest types.
const (
	GameDice          = "dice"
	GameSlots         = "slots"
	GameGuessNumber   = "guess_number"
	GameLuckyTicket   = "lucky_ticket"
	GameQuiz          = "quiz"
	GameDailyStreak   = "daily_streak"
	GameFirstToAnswer = "first_to_answer"
)

// Games lists every game in the order the screens show them.
var Games = []string{GameDice, GameSlots, GameGuessNumber, GameLuckyTicket, GameQuiz, GameDailyStreak, GameFirstToAnswer}

// SlotSymbols is how many symbols each of the three reels carries.
const SlotSymbols = 6

// luckyTickets is how many of the 10^6 six-digit tickets are lucky: the sum
// of the first three digits equals the sum of the last three.
const luckyTickets = 55252

// randomInt draws uniformly from [0, n) with crypto/rand. A customer must
// not be able to predict the next result from the last ones, and a draw from
// math/rand seeded at startup would let them. Tests replace it.
var randomInt = func(n int64) (int64, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		return 0, err
	}
	return value.Int64(), nil
}

// Outcome is what one play drew and whether it won.
type Outcome struct {
	Won bool `json:"won"`
	// Roll is the die (1–6); Reels the three slot symbols (0–5); Secret the
	// number drawn after the customer's Guess; Ticket the six digits; Streak
	// the check-in run including today; Correct the right answer's index.
	Roll    int    `json:"roll,omitempty"`
	Reels   []int  `json:"reels,omitempty"`
	Guess   int    `json:"guess,omitempty"`
	Secret  int    `json:"secret,omitempty"`
	Ticket  string `json:"ticket,omitempty"`
	Streak  int    `json:"streak,omitempty"`
	Choice  *int   `json:"choice,omitempty"`
	Correct *int   `json:"correct,omitempty"`
}

// ErrBadChoice reports a guess or an answer outside the game's range.
var ErrBadChoice = errors.New("the choice is outside the game's range")

// PlayDice rolls one die and wins at or above threshold.
func PlayDice(threshold int) (Outcome, error) {
	roll, err := randomInt(6)
	if err != nil {
		return Outcome{}, err
	}
	outcome := Outcome{Roll: int(roll) + 1}
	outcome.Won = outcome.Roll >= threshold
	return outcome, nil
}

// PlaySlots spins three independent reels and wins on three of a kind.
func PlaySlots() (Outcome, error) {
	reels := make([]int, 3)
	for index := range reels {
		symbol, err := randomInt(SlotSymbols)
		if err != nil {
			return Outcome{}, err
		}
		reels[index] = int(symbol)
	}
	return Outcome{Reels: reels, Won: reels[0] == reels[1] && reels[1] == reels[2]}, nil
}

// PlayGuessNumber draws the secret from 1..upTo after the customer has
// picked, so nothing the customer could see beforehand predicts it.
func PlayGuessNumber(upTo, guess int) (Outcome, error) {
	if guess < 1 || guess > upTo {
		return Outcome{}, ErrBadChoice
	}
	secret, err := randomInt(int64(upTo))
	if err != nil {
		return Outcome{}, err
	}
	outcome := Outcome{Guess: guess, Secret: int(secret) + 1}
	outcome.Won = outcome.Secret == guess
	return outcome, nil
}

// PlayLuckyTicket draws a six-digit ticket.
func PlayLuckyTicket() (Outcome, error) {
	number, err := randomInt(1_000_000)
	if err != nil {
		return Outcome{}, err
	}
	ticket := fmt.Sprintf("%06d", number)
	return Outcome{Ticket: ticket, Won: LuckyTicket(ticket)}, nil
}

// LuckyTicket reports whether a six-digit ticket's halves sum alike.
func LuckyTicket(ticket string) bool {
	if len(ticket) != 6 {
		return false
	}
	sum := func(part string) int {
		total := 0
		for _, digit := range part {
			total += int(digit - '0')
		}
		return total
	}
	return sum(ticket[:3]) == sum(ticket[3:])
}

// AnswerQuestion scores a multiple-choice answer.
func AnswerQuestion(options, correct, choice int) (Outcome, error) {
	if choice < 0 || choice >= options {
		return Outcome{}, ErrBadChoice
	}
	return Outcome{Choice: &choice, Correct: &correct, Won: choice == correct}, nil
}

// CheckIn scores a daily check-in: every every-th day in a row wins.
func CheckIn(streak, every int) Outcome {
	return Outcome{Streak: streak, Won: every > 0 && streak > 0 && streak%every == 0}
}

// WinProbability is the chance one play wins, for the return-to-player
// check. A game of knowledge or speed has no chance to compute, so it is
// taken as certain: the check then holds for the best possible player.
func WinProbability(code string, parameter int) float64 {
	switch code {
	case GameDice:
		if parameter < 1 || parameter > 6 {
			return 1
		}
		return float64(7-parameter) / 6
	case GameSlots:
		return 1 / float64(SlotSymbols*SlotSymbols)
	case GameGuessNumber:
		if parameter < 1 {
			return 1
		}
		return 1 / float64(parameter)
	case GameLuckyTicket:
		return float64(luckyTickets) / 1_000_000
	case GameDailyStreak:
		if parameter < 1 {
			return 1
		}
		return 1 / float64(parameter)
	default:
		return 1
	}
}

// GameRTPPercent is a paid game's expected payout as a percentage of its
// entry fee, or 0 for a free game.
func GameRTPPercent(code string, parameter int, prizeValueMinor, costMinor int64) float64 {
	if costMinor <= 0 {
		return 0
	}
	return WinProbability(code, parameter) * float64(prizeValueMinor) * 100 / float64(costMinor)
}
