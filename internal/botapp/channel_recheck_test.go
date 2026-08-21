package botapp

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/channelgate"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// A cached absence is never trusted, however fresh: the customer on the gate
// screen has most likely just joined and tapped "I have subscribed", and
// bouncing them for five minutes because the cache still says they are out is
// the gate refusing the very thing it asked for.
func TestAFreshCachedAbsenceIsReAsked(t *testing.T) {
	stub := &stubVerifier{member: true}
	app := &App{membership: stub, logger: testLogger()}
	channel := channelFixture("https://t.me/x", "")
	channel.ID = pgtype.UUID{Bytes: [16]byte{4}, Valid: true}

	now := clockFixture()
	known := map[string]dbgen.ChannelMembership{
		uuidText(channel.ID): {
			State:     channelgate.StateAbsent,
			CheckedAt: pgtype.Timestamptz{Time: now.Add(-10 * time.Second), Valid: true},
		},
	}
	member, answered := app.membershipNow(context.Background(), nil, pgtype.UUID{}, channel, known, 42, now)
	if !member || !answered {
		t.Fatalf("a customer who just joined must be let through: %v %v", member, answered)
	}
	if stub.calls != 1 {
		t.Fatalf("telegram must be asked when the cache says absent: %d", stub.calls)
	}
}

// An "I have subscribed" button returns to the purchase that was interrupted,
// whatever it was — a checkout, an add-on, a cart, a gift, or a shop item.
func TestTheChannelGateReturnsToTheInterruptedPurchase(t *testing.T) {
	t.Parallel()
	gate := PurchaseGate{Missing: []ChannelRequirement{{Title: "News", InviteURL: "https://t.me/news"}}}
	for retry, expected := range map[string]string{
		"":                      actionPrefix + "checkout",
		"cart-buy":              actionPrefix + "cart-buy",
		"shop-buy:p1:someone":   actionPrefix + "shop-buy:p1:someone",
		"gift-message:plan:pv1": actionPrefix + "gift-message:plan:pv1",
	} {
		view := channelGateView(LocaleEnglish, gate, retry)
		callbacks := callbackData(view)
		if len(callbacks) == 0 || callbacks[len(callbacks)-1] != expected {
			t.Fatalf("retry %q must lead to %q: %v", retry, expected, callbacks)
		}
	}
}
