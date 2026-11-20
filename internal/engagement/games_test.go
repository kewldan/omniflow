package engagement

import (
	"errors"
	"math"
	"testing"
)

// fixedDraws makes randomInt return the given values in order.
func fixedDraws(t *testing.T, values ...int64) {
	t.Helper()
	original := randomInt
	t.Cleanup(func() { randomInt = original })
	randomInt = func(n int64) (int64, error) {
		if len(values) == 0 {
			t.Fatal("more draws than expected")
		}
		value := values[0]
		values = values[1:]
		if value < 0 || value >= n {
			t.Fatalf("draw %d outside [0, %d)", value, n)
		}
		return value, nil
	}
}

func TestDiceWinsAtOrAboveTheThreshold(t *testing.T) {
	fixedDraws(t, 5, 4)
	if outcome, _ := PlayDice(6); outcome.Roll != 6 || !outcome.Won {
		t.Fatalf("a six = %+v", outcome)
	}
	if outcome, _ := PlayDice(6); outcome.Roll != 5 || outcome.Won {
		t.Fatalf("a five = %+v", outcome)
	}
}

func TestSlotsWinOnThreeOfAKind(t *testing.T) {
	fixedDraws(t, 2, 2, 2, 1, 2, 2)
	if outcome, _ := PlaySlots(); !outcome.Won {
		t.Fatalf("three of a kind = %+v", outcome)
	}
	if outcome, _ := PlaySlots(); outcome.Won {
		t.Fatalf("a mixed line = %+v", outcome)
	}
}

func TestGuessNumberDrawsAfterThePick(t *testing.T) {
	if _, err := PlayGuessNumber(10, 11); !errors.Is(err, ErrBadChoice) {
		t.Fatalf("an out-of-range guess: %v", err)
	}
	fixedDraws(t, 6, 0)
	if outcome, _ := PlayGuessNumber(10, 7); !outcome.Won || outcome.Secret != 7 {
		t.Fatalf("a right guess = %+v", outcome)
	}
	if outcome, _ := PlayGuessNumber(10, 7); outcome.Won || outcome.Secret != 1 {
		t.Fatalf("a wrong guess = %+v", outcome)
	}
}

func TestLuckyTicket(t *testing.T) {
	for ticket, lucky := range map[string]bool{"123321": true, "000000": true, "999999": true, "123456": false, "12345": false} {
		if LuckyTicket(ticket) != lucky {
			t.Fatalf("%s lucky = %v", ticket, !lucky)
		}
	}
	// The probability the RTP check uses is the real count of lucky tickets.
	lucky := 0
	for number := 0; number < 1_000_000; number++ {
		digits := []int{number / 100000 % 10, number / 10000 % 10, number / 1000 % 10, number / 100 % 10, number / 10 % 10, number % 10}
		if digits[0]+digits[1]+digits[2] == digits[3]+digits[4]+digits[5] {
			lucky++
		}
	}
	if lucky != luckyTickets {
		t.Fatalf("lucky tickets = %d, constant says %d", lucky, luckyTickets)
	}
	fixedDraws(t, 1230)
	if outcome, _ := PlayLuckyTicket(); outcome.Ticket != "001230" || outcome.Won {
		t.Fatalf("ticket = %+v", outcome)
	}
}

func TestStreakAndAnswers(t *testing.T) {
	if CheckIn(6, 7).Won || !CheckIn(7, 7).Won || !CheckIn(14, 7).Won {
		t.Fatal("a streak pays on every seventh day")
	}
	if outcome, _ := AnswerQuestion(4, 2, 2); !outcome.Won {
		t.Fatal("a right answer lost")
	}
	if _, err := AnswerQuestion(4, 2, 4); !errors.Is(err, ErrBadChoice) {
		t.Fatalf("an answer past the options: %v", err)
	}
}

func TestGameReturnToPlayer(t *testing.T) {
	cases := map[string]float64{
		GameDice: 1.0 / 6, GameSlots: 1.0 / 36, GameGuessNumber: 0.1, GameLuckyTicket: 0.055252,
		GameDailyStreak: 1.0 / 7, GameQuiz: 1, GameFirstToAnswer: 1,
	}
	parameters := map[string]int{GameDice: 6, GameGuessNumber: 10, GameDailyStreak: 7, GameFirstToAnswer: 3}
	for code, want := range cases {
		if got := WinProbability(code, parameters[code]); math.Abs(got-want) > 1e-9 {
			t.Fatalf("%s win probability = %v, want %v", code, got, want)
		}
	}
	// A 100 RUB prize on a 1-in-6 die with a 50 RUB fee returns a third.
	if rtp := GameRTPPercent(GameDice, 6, 10000, 5000); math.Abs(rtp-100.0/3) > 1e-9 {
		t.Fatalf("rtp = %v", rtp)
	}
	if GameRTPPercent(GameDice, 6, 10000, 0) != 0 {
		t.Fatal("a free game has no return to compute")
	}
}
