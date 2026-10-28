package botapp

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/platform"
	"github.com/valkey-io/valkey-go"
)

func miniLimiter(t *testing.T) *platform.RateLimiter {
	t.Helper()
	server := miniredis.RunT(t)
	client, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{server.Addr()}, DisableCache: true})
	if err != nil {
		t.Fatalf("connect to miniredis: %v", err)
	}
	t.Cleanup(client.Close)
	return platform.NewRateLimiter(client)
}

// Two quick taps on one button arrive as two callbacks with two identifiers,
// and the handlers run concurrently. The claim used to be keyed on the
// identifier alone, so both went through and a top-up or an add-on was
// ordered twice. The tap — chat, message, button — is now claimed as well.
func TestClaimCallbackStopsADoubleTap(t *testing.T) {
	t.Parallel()
	app := New(slog.New(slog.NewTextHandler(io.Discard, nil)), &fakeIdentityStore{}, &fakeRemnawave{}, "")
	app.limiter = miniLimiter(t)
	ctx := context.Background()
	tap := func(id, data string, messageID int) bool {
		return app.claimCallback(ctx, &models.CallbackQuery{ID: id, Data: data}, 42, messageID)
	}

	if !tap("first", "act:topup-pm:manual:50000", 7) {
		t.Fatal("the first tap was refused")
	}
	if tap("second", "act:topup-pm:manual:50000", 7) {
		t.Fatal("a second tap on the same button was let through")
	}
	if tap("first", "act:topup-pm:manual:90000", 7) {
		t.Fatal("a redelivered callback was let through")
	}
	if !tap("third", "act:topup-pm:manual:90000", 7) {
		t.Fatal("a different button on the same message was refused")
	}
	if !tap("fourth", "act:topup-pm:manual:50000", 8) {
		t.Fatal("the same button on another message was refused")
	}
}

// The order key of a tap is stable for the same button on the same message
// within its window, and different otherwise.
func TestTapIdempotencyKeyIsStablePerTap(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 12, 0, 1, 0, time.UTC)
	key := tapIdempotencyKey(42, 7, "act:topup-pm:manual:50000", now)
	if again := tapIdempotencyKey(42, 7, "act:topup-pm:manual:50000", now.Add(time.Minute)); again != key {
		t.Fatal("a duplicate a minute later got a different key")
	}
	for name, other := range map[string]string{
		"another message": tapIdempotencyKey(42, 8, "act:topup-pm:manual:50000", now),
		"another button":  tapIdempotencyKey(42, 7, "act:topup-pm:manual:90000", now),
		"another chat":    tapIdempotencyKey(43, 7, "act:topup-pm:manual:50000", now),
		"a later window":  tapIdempotencyKey(42, 7, "act:topup-pm:manual:50000", now.Add(tapKeyWindow)),
	} {
		if other == key {
			t.Fatalf("%s shares the key", name)
		}
	}
}
