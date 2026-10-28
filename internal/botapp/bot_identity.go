package botapp

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Backoff bounds for looking the bot's own username up again after getMe
// failed. A transient failure at startup is retried within a minute; a
// persistent one is asked about at most hourly.
const (
	identityRetryFirst = 30 * time.Second
	identityRetryMax   = time.Hour
)

// botIdentity holds the bot's public username, which referral links are
// built from.
//
// The username used to be read once, by getMe at startup. When that call
// failed — Telegram briefly unreachable while the container started — referral
// links stayed off until somebody restarted the bot. Now a missing username is
// looked up again when a screen needs it, with backoff so a persistent outage
// costs one call per backoff interval rather than one per screen.
type botIdentity struct {
	mutex    sync.Mutex
	username string
	lookup   func(ctx context.Context) (string, error)
	clock    func() time.Time
	retryAt  time.Time
	backoff  time.Duration
}

func normalizeUsername(username string) string {
	return strings.TrimPrefix(strings.TrimSpace(username), "@")
}

// set records a username the caller already knows.
func (identity *botIdentity) set(username string) {
	identity.mutex.Lock()
	defer identity.mutex.Unlock()
	identity.username = normalizeUsername(username)
}

// useLookup supplies the call that asks Telegram, kept only if none is set.
func (identity *botIdentity) useLookup(lookup func(ctx context.Context) (string, error)) {
	identity.mutex.Lock()
	defer identity.mutex.Unlock()
	if identity.lookup == nil {
		identity.lookup = lookup
	}
}

// get returns the username, asking Telegram for it when it is unknown and the
// backoff allows. An empty result means it is still unknown, and the screen
// renders without a referral link this time.
func (identity *botIdentity) get(ctx context.Context) string {
	identity.mutex.Lock()
	defer identity.mutex.Unlock()
	if identity.username != "" || identity.lookup == nil {
		return identity.username
	}
	now := time.Now()
	if identity.clock != nil {
		now = identity.clock()
	}
	if now.Before(identity.retryAt) {
		return ""
	}
	username, err := identity.lookup(ctx)
	if username = normalizeUsername(username); err == nil && username != "" {
		identity.username, identity.backoff = username, 0
		return username
	}
	switch {
	case identity.backoff == 0:
		identity.backoff = identityRetryFirst
	case identity.backoff < identityRetryMax:
		identity.backoff = min(identity.backoff*2, identityRetryMax)
	}
	identity.retryAt = now.Add(identity.backoff)
	return ""
}
