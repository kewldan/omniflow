package platform

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/valkey-io/valkey-go"
)

func newTestValkeyClient(t *testing.T) valkey.Client {
	t.Helper()
	server := miniredis.RunT(t)
	client, err := valkey.NewClient(valkey.ClientOption{
		InitAddress:  []string{server.Addr()},
		DisableCache: true,
	})
	if err != nil {
		t.Fatalf("connect to miniredis: %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

// A rate limit that only holds under a single caller is not a rate limit; the
// fixed-window counter has to stay exact when many goroutines race the same
// key, which is what an actual abuse burst looks like.
func TestRateLimiterAllowsExactlyTheConfiguredLimitUnderConcurrentLoad(t *testing.T) {
	limiter := NewRateLimiter(newTestValkeyClient(t))
	ctx := context.Background()
	const limit = 10
	const burst = 100

	var allowed atomic.Int64
	group := &sync.WaitGroup{}
	for range burst {
		group.Go(func() {
			ok, err := limiter.Allow(ctx, "burst-scope", "burst-subject", limit, time.Minute)
			if err != nil {
				t.Errorf("allow: %v", err)
				return
			}
			if ok {
				allowed.Add(1)
			}
		})
	}
	group.Wait()

	if got := allowed.Load(); got != limit {
		t.Fatalf("expected exactly %d allowed calls under a %d-request burst, got %d", limit, burst, got)
	}
}

// Once is the replay guard payment webhooks and destructive callbacks build
// on. Under concurrent redelivery exactly one caller may ever win the claim.
func TestOnceIsSingleWinnerUnderConcurrentLoad(t *testing.T) {
	limiter := NewRateLimiter(newTestValkeyClient(t))
	ctx := context.Background()
	const burst = 50

	var won atomic.Int64
	group := &sync.WaitGroup{}
	for range burst {
		group.Go(func() {
			ok, err := limiter.Once(ctx, "burst-once", "shared-token", time.Minute)
			if err != nil {
				t.Errorf("once: %v", err)
				return
			}
			if ok {
				won.Add(1)
			}
		})
	}
	group.Wait()

	if got := won.Load(); got != 1 {
		t.Fatalf("expected exactly one winner under a %d-request replay burst, got %d", burst, got)
	}
}
