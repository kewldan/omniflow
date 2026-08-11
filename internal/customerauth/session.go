// Package customerauth holds the domain rules for customer sign-in and browser
// sessions: how long a session lives, when it rotates, when an action is
// sensitive enough to demand a recent authentication, and how each sign-in
// route proves who is asking.
//
// It contains no persistence and no transport. The PostgreSQL adapter is
// internal/customerauthpg and the HTTP surface is internal/httpapi; both apply
// the rules decided here so the rules stay unit-testable in one place.
package customerauth

import (
	"errors"
	"time"

	"github.com/omniflow/omniflow/internal/websession"
)

// csrfLabel domain-separates the customer panel's double-submit token from the
// operator panel's, which derives its own from the same construction.
const csrfLabel = "omniflow.customer.csrf"

var (
	// ErrSessionExpired reports a session past one of its deadlines.
	ErrSessionExpired = errors.New("session has expired")
	// ErrSessionRevoked reports a session that was explicitly ended.
	ErrSessionRevoked = errors.New("session was revoked")
	// ErrAccountUnavailable reports a session whose customer is suspended or
	// deleted. The session may be perfectly valid; the account is not.
	ErrAccountUnavailable = errors.New("account is not available")
	// ErrReauthenticationRequired reports an action that needs a fresher
	// authentication than the current session has.
	ErrReauthenticationRequired = errors.New("recent authentication is required")
)

// SessionPolicy is the lifetime configuration for customer sessions.
//
// Two independent deadlines run at once, as they do for operators: IdleTimeout
// slides forward on every authenticated request, while AbsoluteTimeout never
// moves, so a session kept warm by a background tab still ends at a fixed
// horizon and a stolen cookie has a bounded life.
type SessionPolicy struct {
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	// RotateAfter is how long a session token may be used before it is swapped
	// for a fresh one, shortening the window in which a token captured from a
	// log or a proxy stays replayable.
	RotateAfter time.Duration
	// ReauthWindow is how recently a session must have authenticated before it
	// may perform a sensitive action. See RequiresReauthentication.
	ReauthWindow time.Duration
}

// DefaultSessionPolicy is deliberately more generous than the operator panel's.
//
// The operator panel expires in thirty minutes because it moves money and reads
// other people's data, and an operator signing in again is a few seconds of a
// working day. A customer checking how many days are left on their subscription
// is not doing either of those things, and a panel that logs them out over lunch
// simply pushes them back to the bot. The absolute horizon does the security
// work here instead of the idle one, backed by rotation and by re-authentication
// on the few actions that genuinely warrant it.
var DefaultSessionPolicy = SessionPolicy{
	IdleTimeout:     14 * 24 * time.Hour,
	AbsoluteTimeout: 60 * 24 * time.Hour,
	RotateAfter:     24 * time.Hour,
	ReauthWindow:    15 * time.Minute,
}

// NewSessionToken returns a URL-safe token and the digest to store for it.
func NewSessionToken() (token string, digest []byte, err error) { return websession.NewToken() }

// HashSessionToken returns the storage digest for a session token.
func HashSessionToken(token string) []byte { return websession.HashToken(token) }

// NewCSRFSecret returns a per-session secret for the double-submit token.
func NewCSRFSecret() ([]byte, error) { return websession.NewCSRFSecret() }

// CSRFToken derives the token the browser echoes on every unsafe request.
func CSRFToken(secret []byte) string { return websession.CSRFToken(secret, csrfLabel) }

// ValidCSRFToken reports whether a submitted token matches the session secret.
func ValidCSRFToken(secret []byte, submitted string) bool {
	return websession.ValidCSRFToken(secret, csrfLabel, submitted)
}

// SessionState is the subset of a stored session the policy needs to judge it.
type SessionState struct {
	// CreatedAt is when the session authenticated. It is the reference point for
	// re-authentication, not LastSeenAt: using activity would mean a session
	// that has been open all day counts as freshly authenticated.
	CreatedAt       time.Time
	RotatedAt       time.Time
	IdleExpiresAt   time.Time
	AbsoluteExpires time.Time
	RevokedAt       *time.Time
}

// Validate reports why a session is unusable, or nil when it is live.
//
// Revocation is checked before expiry so the more specific reason survives when
// both apply.
func (policy SessionPolicy) Validate(state SessionState, now time.Time) error {
	if state.RevokedAt != nil {
		return ErrSessionRevoked
	}
	if !now.Before(state.AbsoluteExpires) || !now.Before(state.IdleExpiresAt) {
		return ErrSessionExpired
	}
	return nil
}

// NextIdleDeadline is the slid-forward inactivity deadline, clamped so it can
// never exceed the absolute deadline.
func (policy SessionPolicy) NextIdleDeadline(now, absolute time.Time) time.Time {
	deadline := now.Add(policy.IdleTimeout)
	if deadline.After(absolute) {
		return absolute
	}
	return deadline
}

// ShouldRotate reports whether a live session is due for a new token.
func (policy SessionPolicy) ShouldRotate(state SessionState, now time.Time) bool {
	if policy.RotateAfter <= 0 {
		return false
	}
	return now.Sub(state.RotatedAt) >= policy.RotateAfter
}

// RequiresReauthentication reports whether a sensitive action must be refused
// until the customer signs in again.
//
// The actions this guards are the ones a stolen, still-live cookie could
// otherwise use to lock the real owner out of their own subscription: rotating
// the subscription link, removing every device at once, unlinking a sign-in
// method. Each is destructive, none is reversible from the panel, and every one
// of them is something a customer does rarely and deliberately — so paying for a
// fresh sign-in is a small cost in exchange for a session left open on a shared
// machine not being enough on its own.
//
// A zero ReauthWindow disables the requirement, which is what a policy built for
// tests wants.
func (policy SessionPolicy) RequiresReauthentication(state SessionState, now time.Time) bool {
	if policy.ReauthWindow <= 0 {
		return false
	}
	return now.Sub(state.CreatedAt) > policy.ReauthWindow
}
