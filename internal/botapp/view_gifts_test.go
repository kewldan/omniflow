package botapp

import (
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

func TestGiftCodeViewShowsTheCodeOnce(t *testing.T) {
	t.Parallel()
	purchase := GiftPurchase{OrderID: "abcdef12-0000-0000-0000-000000000000", Code: "ABCD-EFGH-IJKL-MNOP", CodeHint: "MNOP", ExpiresAt: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)}
	paid := OrderSummary{ID: purchase.OrderID, State: commerce.OrderPaid}
	view := giftCodeView(LocaleEnglish, purchase, paid)
	if !strings.Contains(view.Text, "<code>ABCD-EFGH-IJKL-MNOP</code>") {
		t.Fatalf("the freshly minted code must be shown: %s", view.Text)
	}
	if strings.Contains(view.Text, "already exists") {
		t.Fatalf("a new gift is not a replay: %s", view.Text)
	}
	unpaid := OrderSummary{ID: purchase.OrderID, State: commerce.OrderPending, ExternalMinor: 100}
	if view := giftCodeView(LocaleEnglish, purchase, unpaid); !strings.Contains(view.Text, "redeemable as soon as the order is paid") {
		t.Fatalf("an unpaid gift must say when it becomes redeemable: %s", view.Text)
	}
}

func TestGiftCodeViewNeverRendersAnEmptyCode(t *testing.T) {
	t.Parallel()
	replayed := GiftPurchase{OrderID: "abcdef12-0000-0000-0000-000000000000", CodeHint: "MNOP"}
	view := giftCodeView(LocaleEnglish, replayed, OrderSummary{ID: replayed.OrderID, State: commerce.OrderPaid})
	if strings.Contains(view.Text, "<code></code>") {
		t.Fatalf("an empty code must never be rendered: %s", view.Text)
	}
	if !strings.Contains(view.Text, "already exists") || !strings.Contains(view.Text, "••MNOP") {
		t.Fatalf("a replayed gift must be named by its hint: %s", view.Text)
	}
	if !strings.Contains(strings.Join(callbackData(view), " "), actionPrefix+"order:"+replayed.OrderID) {
		t.Fatalf("a replayed gift must link to its order: %v", callbackData(view))
	}
}
