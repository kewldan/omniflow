package botapp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	telegram "github.com/go-telegram/bot"
)

// The pacer lets a second's worth of messages go at once and then holds the
// configured rate, so a broadcast cannot outrun Telegram's bot-wide limit.
func TestDeliveryPacerHoldsTheRate(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	pacer := newDeliveryPacer(4, func() time.Time { return now })
	for index := range 4 {
		if wait, _ := pacer.reserve(); wait != 0 {
			t.Fatalf("send %d of the burst waited %v", index+1, wait)
		}
	}
	if wait, _ := pacer.reserve(); wait != 250*time.Millisecond {
		t.Fatalf("the send after the burst waits %v, want a quarter second", wait)
	}
	if wait, _ := pacer.reserve(); wait != 500*time.Millisecond {
		t.Fatalf("the next one waits %v, want half a second", wait)
	}
}

// A flood wait pauses every send. A short one is waited out; a long one is
// reported so the caller defers the message instead of sleeping on it.
func TestDeliveryPacerPausesEverySend(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	pacer := newDeliveryPacer(DefaultDeliveryRate, func() time.Time { return now })
	pacer.pause(now.Add(5 * time.Second))
	if wait, until := pacer.reserve(); wait != 5*time.Second || !until.IsZero() {
		t.Fatalf("a five-second pause gave wait %v, until %v", wait, until)
	}
	pacer.pause(now.Add(2 * time.Minute))
	if _, until := pacer.reserve(); !until.Equal(now.Add(2 * time.Minute)) {
		t.Fatalf("a long pause was not reported: %v", until)
	}
	if _, paused := pacer.paused(); !paused {
		t.Fatal("a long pause does not show as paused")
	}
}

// A 429 is the bot's, not the customer's. It used to be recorded on the
// customer who happened to receive it, while the next send ran into the same
// limit; now it pauses the sender and touches no customer state — the sender
// below has no store at all, and would fail on any attempt to record one.
func TestSenderPausesOnFloodWithoutBlamingTheCustomer(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 120","parameters":{"retry_after":120}}`))
	}))
	t.Cleanup(server.Close)
	client, err := telegram.New("test-token", telegram.WithServerURL(server.URL), telegram.WithSkipGetMe())
	if err != nil {
		t.Fatalf("telegram client: %v", err)
	}
	sender := NewSender(client, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	first := sender.Send(context.Background(), "customer-a", 1, View{Text: "hello"})
	retryAfter, flood := floodDeferral(first)
	if !flood || time.Until(retryAfter) < time.Minute {
		t.Fatalf("a 429 with retry_after 120 returned %v", first)
	}
	second := sender.Send(context.Background(), "customer-b", 2, View{Text: "hello"})
	if _, flood := floodDeferral(second); !flood {
		t.Fatalf("a send during the pause returned %v", second)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("Telegram was asked %d times during its own pause, want once", got)
	}
	if _, paused := sender.Paused(); !paused {
		t.Fatal("the sender does not report the pause")
	}
	if !errors.Is(second, errFloodPaused) {
		t.Fatalf("the paused send's cause is %v", second)
	}
}
