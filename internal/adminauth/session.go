package adminauth

import (
	"errors"
	"time"

	"github.com/omniflow/omniflow/internal/websession"
)

// SessionPolicy is the lifetime configuration for operator sessions.
//
// Two independent deadlines run at once. IdleTimeout slides forward on every
// authenticated request, so an unattended browser stops being useful quickly.
// AbsoluteTimeout never moves, so a session that is kept warm by a background
// tab still ends at a fixed horizon and a stolen cookie has a bounded life.
type SessionPolicy struct {
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	// RotateAfter is how long a session token may be used before it is swapped
	// for a fresh one. Rotation shortens the window in which a token captured
	// from a log or a proxy remains replayable.
	RotateAfter time.Duration
	// ChallengeTimeout bounds a session that has passed the password factor but
	// not yet the second one.
	ChallengeTimeout time.Duration
}

// DefaultSessionPolicy is tuned for a panel that controls money and customer
// data: short inactivity window, one working day of absolute life.
var DefaultSessionPolicy = SessionPolicy{
	IdleTimeout:      30 * time.Minute,
	AbsoluteTimeout:  12 * time.Hour,
	RotateAfter:      15 * time.Minute,
	ChallengeTimeout: 5 * time.Minute,
}

const (
	// SessionTokenBytes is the entropy in a session token. 256 bits makes the
	// token unguessable independently of any rate limit.
	SessionTokenBytes = websession.TokenBytes
	// CSRFSecretBytes is the per-session secret the double-submit token is
	// derived from.
	CSRFSecretBytes = websession.CSRFSecretBytes

	// csrfLabel domain-separates the operator panel's double-submit token from
	// the customer panel's, which derives its own from the same construction.
	csrfLabel = "omniflow.admin.csrf"
)

var (
	// ErrSessionExpired reports a session past one of its deadlines.
	ErrSessionExpired = errors.New("session has expired")
	// ErrSessionRevoked reports a session that was explicitly ended.
	ErrSessionRevoked = errors.New("session was revoked")
	// ErrChallengePending reports a session that has not completed its second
	// factor and therefore authorizes nothing but the challenge endpoints.
	ErrChallengePending = errors.New("session has not completed its second factor")
)

// NewSessionToken returns a URL-safe token and the digest to store for it.
//
// Only the digest is persisted. A database read therefore never yields a usable
// cookie, and an offline backup cannot be replayed against a live installation.
func NewSessionToken() (token string, digest []byte, err error) {
	return websession.NewToken()
}

// HashSessionToken returns the storage digest for a session token.
//
// SHA-256 is correct here for the same reason it is for recovery codes: the
// token is 256 bits of uniform entropy, so there is nothing for a slow hash to
// defend against, and lookups happen on every request.
func HashSessionToken(token string) []byte { return websession.HashToken(token) }

// NewCSRFSecret returns a per-session secret for the double-submit token.
func NewCSRFSecret() ([]byte, error) { return websession.NewCSRFSecret() }

// CSRFToken derives the token handed to the browser from the session's secret.
//
// Binding it to the session means a token minted for one session cannot be
// replayed inside another, which a bare random double-submit cookie allows.
func CSRFToken(secret []byte) string { return websession.CSRFToken(secret, csrfLabel) }

// ValidCSRFToken reports whether a submitted token matches the session secret.
func ValidCSRFToken(secret []byte, submitted string) bool {
	return websession.ValidCSRFToken(secret, csrfLabel, submitted)
}

// SessionDeadlines computes the two expiries for a session created at `now`.
// A session still completing its second factor gets the shorter challenge
// window instead of the full idle timeout.
func (policy SessionPolicy) SessionDeadlines(now time.Time, pendingChallenge bool) (idle, absolute time.Time) {
	idleWindow := policy.IdleTimeout
	if pendingChallenge {
		idleWindow = policy.ChallengeTimeout
	}
	return now.Add(idleWindow), now.Add(policy.AbsoluteTimeout)
}

// NextIdleDeadline is the slid-forward inactivity deadline, clamped so it can
// never exceed the absolute deadline. Without the clamp, a request made just
// before the absolute horizon would push the idle window past it and leave a
// session alive that the absolute limit had already ended.
func (policy SessionPolicy) NextIdleDeadline(now, absolute time.Time) time.Time {
	deadline := now.Add(policy.IdleTimeout)
	if deadline.After(absolute) {
		return absolute
	}
	return deadline
}

// SessionState is the subset of a stored session the policy needs to judge it.
type SessionState struct {
	PendingChallenge bool
	RotatedAt        time.Time
	IdleExpiresAt    time.Time
	AbsoluteExpires  time.Time
	RevokedAt        *time.Time
}

// Validate reports why a session is unusable, or nil when it is live.
//
// Revocation is checked before expiry so the audit trail records the more
// specific reason when both apply.
func (policy SessionPolicy) Validate(state SessionState, now time.Time) error {
	if state.RevokedAt != nil {
		return ErrSessionRevoked
	}
	if !now.Before(state.AbsoluteExpires) || !now.Before(state.IdleExpiresAt) {
		return ErrSessionExpired
	}
	return nil
}

// ShouldRotate reports whether a live session is due for a new token.
func (policy SessionPolicy) ShouldRotate(state SessionState, now time.Time) bool {
	if policy.RotateAfter <= 0 {
		return false
	}
	return now.Sub(state.RotatedAt) >= policy.RotateAfter
}

// LockoutPolicy is the response to repeated failed sign-ins for one account.
//
// The backoff is exponential in the number of consecutive failures and capped,
// which slows credential stuffing to a crawl without giving an attacker a way
// to lock a known operator out permanently by failing on purpose.
type LockoutPolicy struct {
	// Threshold is how many consecutive failures are tolerated before the
	// first lockout applies.
	Threshold int
	// BaseDelay is the lockout after the threshold is first crossed.
	BaseDelay time.Duration
	// MaxDelay caps the exponential growth.
	MaxDelay time.Duration
}

// DefaultLockoutPolicy tolerates a handful of typos, then escalates quickly.
var DefaultLockoutPolicy = LockoutPolicy{
	Threshold: 5,
	BaseDelay: time.Minute,
	MaxDelay:  time.Hour,
}

// LockoutUntil returns the deadline an account is locked until after
// `failures` consecutive failed attempts, or nil when it stays unlocked.
func (policy LockoutPolicy) LockoutUntil(failures int, now time.Time) *time.Time {
	if policy.Threshold <= 0 || failures < policy.Threshold {
		return nil
	}
	delay := policy.BaseDelay
	// Each failure past the threshold doubles the wait, stopping as soon as the
	// cap is reached so a large failure count cannot overflow the duration.
	for range failures - policy.Threshold {
		if delay >= policy.MaxDelay {
			break
		}
		delay *= 2
	}
	if delay > policy.MaxDelay {
		delay = policy.MaxDelay
	}
	deadline := now.Add(delay)
	return &deadline
}

// Locked reports whether a lockout deadline is still in force.
func Locked(lockedUntil *time.Time, now time.Time) bool {
	return lockedUntil != nil && now.Before(*lockedUntil)
}
