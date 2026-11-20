package botapp

import (
	"strings"
	"testing"
	"time"
)

func TestTrafficResetIsOfferedAboveTheWayBack(t *testing.T) {
	t.Parallel()
	subscription, _ := addonFixture()
	detail := subscriptionDetailView(LocaleEnglish, subscription, time.Now().UTC(), false)
	before := callbackData(detail)
	view := withTrafficResetButton(LocaleEnglish, detail, subscription.Slot, 9900, "RUB")
	after := callbackData(view)
	if len(after) != len(before)+1 {
		t.Fatalf("the reset button was not added: %v", after)
	}
	if after[len(after)-2] != actionPrefix+"reset-ask:2" {
		t.Fatalf("the reset button must sit just above the last row and name the slot: %v", after)
	}
	if after[len(after)-1] != before[len(before)-1] {
		t.Fatalf("the way back must stay last: %v", after)
	}
}

func TestTrafficResetConfirmationStatesThePriceAndItsSource(t *testing.T) {
	t.Parallel()
	subscription, _ := addonFixture()
	covered := trafficResetConfirmView(LocaleEnglish, subscription, 9900, "RUB", 50000, "2")
	if !strings.Contains(covered.Text, "99 RUB") || !strings.Contains(covered.Text, "Paid from your wallet") {
		t.Fatalf("the price and the wallet source must be stated: %s", covered.Text)
	}
	callbacks := callbackData(covered)
	if callbacks[0] != actionPrefix+"reset-buy:2" {
		t.Fatalf("confirming must name the slot: %v", callbacks)
	}
	short := trafficResetConfirmView(LocaleRussian, subscription, 9900, "RUB", 100, "2")
	if !strings.Contains(short.Text, "выберете способ оплаты") {
		t.Fatalf("a wallet that does not cover it must say a provider pays: %s", short.Text)
	}
	if !consequentialActions["reset-buy"] {
		t.Fatal("buying a reset moves money and must never run twice for one callback")
	}
}
