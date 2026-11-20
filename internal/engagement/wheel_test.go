package engagement

import (
	"math"
	"testing"
)

func TestExpectedValueAndRTP(t *testing.T) {
	t.Parallel()
	prizes := []Prize{
		{Kind: KindNothing, Weight: 50, Active: true},
		{Kind: KindWallet, Value: 100, Weight: 40, Active: true},
		{Kind: KindDays, Value: 3, Weight: 10, Active: true},
		{Kind: KindWallet, Value: 1_000_000, Weight: 1000, Active: false},
	}
	// (40*100 + 10*3*50) / 100 = 55
	if value := ExpectedValue(prizes, 50); math.Abs(value-55) > 1e-9 {
		t.Fatalf("expected value = %v", value)
	}
	if rtp := RTPPercent(prizes, 50, 100); math.Abs(rtp-55) > 1e-9 {
		t.Fatalf("rtp = %v", rtp)
	}
	if RTPPercent(prizes, 50, 0) != 0 {
		t.Fatal("a free spin has an RTP")
	}
}

func TestDrawFollowsWeights(t *testing.T) {
	t.Parallel()
	prizes := []Prize{
		{ID: "a", Kind: KindNothing, Weight: 9, Active: true},
		{ID: "b", Kind: KindWallet, Value: 1, Weight: 1, Active: true},
		{ID: "off", Kind: KindWallet, Value: 1, Weight: 1000, Active: false},
	}
	counts := map[string]int{}
	for range 20000 {
		prize, err := Draw(prizes)
		if err != nil {
			t.Fatal(err)
		}
		counts[prize.ID]++
	}
	if counts["off"] != 0 || counts["b"] < 1500 || counts["b"] > 2500 {
		t.Fatalf("counts = %v", counts)
	}
	if _, err := Draw([]Prize{{Weight: 1}}); err == nil {
		t.Fatal("an inactive table drew a prize")
	}
}

func TestPrizeValidation(t *testing.T) {
	t.Parallel()
	good := Prize{LabelRU: "100 ₽", LabelEN: "100 RUB", Kind: KindWallet, Value: 10000, Weight: 5}
	if good.Validate() != nil {
		t.Fatal("a valid prize was refused")
	}
	for _, bad := range []Prize{
		{LabelRU: "x", LabelEN: "x", Kind: KindNothing, Value: 5, Weight: 1},
		{LabelRU: "x", LabelEN: "x", Kind: KindDays, Value: 400, Weight: 1},
		{LabelRU: "", LabelEN: "x", Kind: KindNothing, Weight: 1},
		{LabelRU: "x", LabelEN: "x", Kind: KindWallet, Value: 1, Weight: 0},
	} {
		if bad.Validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
