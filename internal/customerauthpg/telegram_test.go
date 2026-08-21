package customerauthpg

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// newUsernameService builds a Service over a pool that is never dialled, which
// is all BotUsername needs: it talks to the bot API, not to PostgreSQL.
func newUsernameService(t *testing.T, apiURL string, clock func() time.Time) *Service {
	t.Helper()
	pool := newUndialledPool(t)
	service, err := New(pool, bytes.Repeat([]byte{3}, 32), Options{
		TelegramBotToken: "000000:unit-token", TelegramAPIURL: apiURL, Clock: clock,
	})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	return service
}

// One transient failure used to be cached for the life of the process by a
// sync.Once, which hid the Telegram button on every sign-in screen until a
// restart. A failure must be retried; only a success is kept.
func TestBotUsernameRetriesAfterAFailureAndKeepsASuccess(t *testing.T) {
	var calls atomic.Int32
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if !healthy.Load() {
			http.Error(writer, "bad gateway", http.StatusBadGateway)
			return
		}
		_, _ = writer.Write([]byte(`{"ok":true,"result":{"username":"@omniflow_bot"}}`))
	}))
	defer server.Close()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	service := newUsernameService(t, server.URL, func() time.Time { return now })
	ctx := context.Background()

	if got := service.BotUsername(ctx); got != "" {
		t.Fatalf("a failed getMe produced %q", got)
	}
	// Inside the retry window the failure is remembered rather than hammered.
	if got := service.BotUsername(ctx); got != "" {
		t.Fatalf("second call inside the retry window produced %q", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("getMe was called %d times inside the retry window, want 1", calls.Load())
	}

	healthy.Store(true)
	now = now.Add(botUsernameRetryAfter)
	if got := service.BotUsername(ctx); got != "omniflow_bot" {
		t.Fatalf("after the retry window the username is %q, want omniflow_bot", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("getMe was called %d times, want 2", calls.Load())
	}

	// A success is kept: later calls do not go back to the bot API even if it
	// has since become unhealthy again.
	healthy.Store(false)
	now = now.Add(time.Hour)
	if got := service.BotUsername(ctx); got != "omniflow_bot" {
		t.Fatalf("a cached username was lost: %q", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("a cached success was re-fetched: %d calls", calls.Load())
	}
}

func TestBotUsernameTreatsAnEmptyUsernameAsAFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = writer.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	service := newUsernameService(t, server.URL, func() time.Time { return now })
	if got := service.BotUsername(context.Background()); got != "" {
		t.Fatalf("an answer with no username produced %q", got)
	}
	now = now.Add(botUsernameRetryAfter)
	_ = service.BotUsername(context.Background())
	if calls.Load() != 2 {
		t.Fatalf("an empty answer was cached as a success: %d calls", calls.Load())
	}
}

func TestBotUsernameIsEmptyWithoutAToken(t *testing.T) {
	pool := newUndialledPool(t)
	service, err := New(pool, bytes.Repeat([]byte{3}, 32), Options{})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	if got := service.BotUsername(context.Background()); got != "" {
		t.Fatalf("no token produced a username: %q", got)
	}
}
