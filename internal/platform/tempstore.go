package platform

import (
	"context"
	"fmt"
	"time"

	"github.com/valkey-io/valkey-go"
)

// TemporaryStore holds short-lived state in Valkey: a forwarding entry written
// at session rotation, a lock, a mapping that only needs to outlive a burst of
// in-flight requests.
//
// It is deliberately tiny. PostgreSQL is the only durable source of truth, and
// anything stored here must be something the system can lose without losing a
// fact — which is why every entry carries a TTL and there is no method to
// write one without.
type TemporaryStore struct{ client valkey.Client }

// NewTemporaryStore wraps a Valkey client.
func NewTemporaryStore(client valkey.Client) *TemporaryStore {
	return &TemporaryStore{client: client}
}

// TemporaryStore exposes the limiter's Valkey connection as a store, so a
// component handed only the limiter can keep temporary state beside it
// without a second connection being threaded through every constructor.
func (limiter *RateLimiter) TemporaryStore() *TemporaryStore {
	if limiter == nil || limiter.client == nil {
		return nil
	}
	return &TemporaryStore{client: limiter.client}
}

// Claim writes value under key only when nothing is there yet, and reports
// whether this caller is the one who wrote it. Concurrent claimants of one key
// get exactly one true.
func (store *TemporaryStore) Claim(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	if store == nil || store.client == nil {
		return false, fmt.Errorf("temporary store is unavailable")
	}
	response := store.client.Do(ctx, store.client.B().Set().Key(prefixed(key)).Value(value).Nx().Px(ttl).Build())
	if err := response.Error(); err != nil {
		if valkey.IsValkeyNil(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Lookup reads an entry. A missing key is (“”, false, nil); an unreachable
// store is an error, because "absent" and "could not ask" are different answers
// and the caller decides what each means.
func (store *TemporaryStore) Lookup(ctx context.Context, key string) (string, bool, error) {
	if store == nil || store.client == nil {
		return "", false, fmt.Errorf("temporary store is unavailable")
	}
	value, err := store.client.Do(ctx, store.client.B().Get().Key(prefixed(key)).Build()).ToString()
	if err != nil {
		if valkey.IsValkeyNil(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return value, true, nil
}

// Forget removes an entry before its TTL would.
func (store *TemporaryStore) Forget(ctx context.Context, key string) error {
	if store == nil || store.client == nil {
		return fmt.Errorf("temporary store is unavailable")
	}
	return store.client.Do(ctx, store.client.B().Del().Key(prefixed(key)).Build()).Error()
}

func prefixed(key string) string { return "omniflow:tmp:" + key }
