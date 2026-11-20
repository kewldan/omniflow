//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/engagement"
)

func (harness *harness) agedCustomer(ctx context.Context, t *testing.T) string {
	t.Helper()
	customer := harness.customer(ctx, t)
	if _, err := harness.pool.Exec(ctx, `UPDATE users SET created_at = now() - interval '30 days' WHERE id = $1::uuid`, customer); err != nil {
		t.Fatal(err)
	}
	return customer
}

// The wheel refuses a prize table above the target return, gives the free
// spin first and charges the next, stops at the daily limit, pays every
// prize through the ledger once per spin, and refuses an account too new.
func TestFortuneWheelLimitsAndPays(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	service := engagement.New(harness.pool, store)
	actor := harness.operator(ctx, t, "wheel@omniflow.test")
	settings := engagement.Settings{Currency: "RUB", DayValueMinor: 1000, WheelEnabled: true, SpinCostMinor: 5000,
		FreeSpinsPerDay: 1, MaxSpinsPerDay: 2, TargetRTPPercent: 80, MinimumAccountAgeH: 24}
	generous := []engagement.Prize{{LabelRU: "60 ₽", LabelEN: "60 RUB", Kind: engagement.KindWallet, Value: 6000, Weight: 1, Active: true}}
	if err := service.SaveWheel(ctx, settings, generous, actor); !errors.Is(err, engagement.ErrRTP) {
		t.Fatalf("a 120%% table: %v", err)
	}
	table := []engagement.Prize{
		{LabelRU: "30 ₽", LabelEN: "30 RUB", Kind: engagement.KindWallet, Value: 3000, Weight: 1, Active: true},
	}
	if err := service.SaveWheel(ctx, settings, table, actor); err != nil {
		t.Fatal(err)
	}

	young := harness.customer(ctx, t)
	if _, err := service.SpinWheel(ctx, young, "spin-young-1"); !errors.Is(err, engagement.ErrTooNew) {
		t.Fatalf("a new account spun: %v", err)
	}
	player := harness.agedCustomer(ctx, t)
	first, err := service.SpinWheel(ctx, player, "spin-key-0001")
	if err != nil || !first.Free || first.Prize.Value != 3000 {
		t.Fatalf("free spin = %+v (%v)", first, err)
	}
	replay, err := service.SpinWheel(ctx, player, "spin-key-0001")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v (%v)", replay, err)
	}
	if balance := harness.walletBalance(ctx, t, player); balance != 3000 {
		t.Fatalf("balance after the free spin = %d", balance)
	}
	if _, err = service.SpinWheel(ctx, player, "spin-key-0002"); !errors.Is(err, engagement.ErrInsufficient) {
		t.Fatalf("a paid spin with 30 RUB: %v", err)
	}
	harness.seedWallet(ctx, t, player, "wheel-topup", 2000)
	second, err := service.SpinWheel(ctx, player, "spin-key-0003")
	if err != nil || second.Free || second.CostMinor != 5000 {
		t.Fatalf("paid spin = %+v (%v)", second, err)
	}
	if balance := harness.walletBalance(ctx, t, player); balance != 3000+2000-5000+3000 {
		t.Fatalf("balance after the paid spin = %d", balance)
	}
	if _, err = service.SpinWheel(ctx, player, "spin-key-0004"); !errors.Is(err, engagement.ErrLimit) {
		t.Fatalf("over the daily limit: %v", err)
	}
	state, err := service.Wheel(ctx, player)
	if err != nil || state.SpinsLeft != 0 || len(state.RecentOutcomes) != 2 {
		t.Fatalf("state = %+v (%v)", state, err)
	}
}

// A poll takes one answer per customer and pays its reward once, and only
// to an account old enough.
func TestPollRewardsOnce(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	store := harness.commerceStore(t, testOptions())
	service := engagement.New(harness.pool, store)
	actor := harness.operator(ctx, t, "polls@omniflow.test")
	poll, err := service.CreatePoll(ctx, engagement.Poll{
		QuestionRU: "Как вам скорость?", QuestionEN: "How is the speed?",
		Options:    []engagement.Option{{RU: "Хорошо", EN: "Good"}, {RU: "Плохо", EN: "Bad"}},
		RewardKind: engagement.KindWallet, RewardValue: 1500, EndsAt: time.Now().Add(time.Hour),
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	voter := harness.agedCustomer(ctx, t)
	if rewarded, err := service.Answer(ctx, voter, poll.ID, 0); err != nil || !rewarded {
		t.Fatalf("answer = %v (%v)", rewarded, err)
	}
	if _, err = service.Answer(ctx, voter, poll.ID, 1); !errors.Is(err, engagement.ErrAnswered) {
		t.Fatalf("a second answer: %v", err)
	}
	if _, err = service.Answer(ctx, voter, poll.ID, 7); err == nil {
		t.Fatal("an option out of range was accepted")
	}
	newcomer := harness.customer(ctx, t)
	if rewarded, err := service.Answer(ctx, newcomer, poll.ID, 1); err != nil || rewarded {
		t.Fatalf("a new account was rewarded: %v (%v)", rewarded, err)
	}
	if balance := harness.walletBalance(ctx, t, voter); balance != 1500 {
		t.Fatalf("reward balance = %d", balance)
	}
	// A second rewarded customer on the same poll is paid as well.
	second := harness.agedCustomer(ctx, t)
	if rewarded, err := service.Answer(ctx, second, poll.ID, 0); err != nil || !rewarded {
		t.Fatalf("second voter = %v (%v)", rewarded, err)
	}
	all, err := service.Polls(ctx, "")
	if err != nil || len(all) != 1 || all[0].Counts[0] != 2 || all[0].Counts[1] != 1 {
		t.Fatalf("results = %+v (%v)", all, err)
	}
	if err = service.ClosePoll(ctx, poll.ID, actor); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Answer(ctx, harness.agedCustomer(ctx, t), poll.ID, 0); !errors.Is(err, engagement.ErrClosed) {
		t.Fatalf("answer to a closed poll: %v", err)
	}
}
