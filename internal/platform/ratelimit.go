package platform

import (
	"context"
	"fmt"
	"time"

	"github.com/valkey-io/valkey-go"
)

type RateLimiter struct{ client valkey.Client }

func NewRateLimiter(client valkey.Client) *RateLimiter { return &RateLimiter{client: client} }

func (limiter *RateLimiter) Allow(ctx context.Context, scope, subject string, limit int64, window time.Duration) (bool, error) {
	if limiter == nil || limiter.client == nil {
		return false, fmt.Errorf("rate limiter is unavailable")
	}
	key := "omniflow:rate:" + scope + ":" + subject
	script := `local current = redis.call('INCR', KEYS[1]); if current == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end; return current`
	current, err := limiter.client.Do(ctx, limiter.client.B().Eval().Script(script).Numkeys(1).Key(key).Arg(fmt.Sprintf("%d", window.Milliseconds())).Build()).AsInt64()
	if err != nil {
		return false, err
	}
	return current <= limit, nil
}
