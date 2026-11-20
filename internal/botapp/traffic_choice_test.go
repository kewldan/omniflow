package botapp

import (
	"slices"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/accountcheckout"
)

func TestTrafficStepsStayInsideTheRange(t *testing.T) {
	steps := trafficSteps(accountcheckout.TrafficOffer{MinGB: 30, MaxGB: 300, PricePerGBMinor: 100})
	if want := []int{30, 50, 100, 200, 300}; !slices.Equal(steps, want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
	if steps = trafficSteps(accountcheckout.TrafficOffer{MinGB: 5, MaxGB: 5, PricePerGBMinor: 1}); !slices.Equal(steps, []int{5}) {
		t.Fatalf("a one-value range = %v", steps)
	}
}

func TestTheCheckoutShowsTheTrafficChoice(t *testing.T) {
	own := int64(10) << 30
	offer := accountcheckout.TrafficOffer{Currency: "RUB", OwnBytes: &own, MinGB: 20, MaxGB: 200, PricePerGBMinor: 100}
	base := View{
		Text:     "title\n\nplan\nperiod\nmethod\n\nsubtotal",
		Keyboard: keyboard(row(actionButton("pay", "confirm")), row(actionButton("promo", "promo"))),
	}
	view := withTrafficChoice(base, LocaleEnglish, offer, CheckoutSession{TrafficGB: 50})
	if !strings.Contains(view.Text, "method\nTraffic: <b>50 GB</b>\n\nsubtotal") {
		t.Fatalf("text = %q", view.Text)
	}
	if got := view.Keyboard.InlineKeyboard[1][0].CallbackData; !strings.HasSuffix(got, "traffic") {
		t.Fatalf("second row = %q", got)
	}
	plain := withTrafficChoice(View{Text: "x"}, LocaleEnglish, accountcheckout.TrafficOffer{}, CheckoutSession{})
	if plain.Text != "x" {
		t.Fatal("a version without a choice changed the summary")
	}
}
