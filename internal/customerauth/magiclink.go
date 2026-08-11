package customerauth

import (
	"errors"
	"time"

	"github.com/omniflow/omniflow/internal/websession"
)

var (
	// ErrMagicLinkUnavailable reports the fallback being switched off for this
	// installation.
	ErrMagicLinkUnavailable = errors.New("magic-link sign-in is not enabled")
	// ErrMagicLinkInvalid reports a token that is unknown, already used, or past
	// its expiry. The three are one error on purpose: telling a caller which of
	// them applied would confirm that a token once existed.
	ErrMagicLinkInvalid = errors.New("sign-in link is not valid")
	// ErrMagicLinkThrottled reports too many requests for one customer.
	ErrMagicLinkThrottled = errors.New("too many sign-in links requested")
)

const (
	// MagicLinkLifetime is how long a delivered link stays usable.
	//
	// Short, because the link is a bearer credential sitting in a chat history:
	// anyone who later reads that chat holds it. Ten minutes is comfortably
	// longer than switching from a browser to Telegram and back, and short
	// enough that a link found later is already dead.
	MagicLinkLifetime = 10 * time.Minute

	// MagicLinkRequestWindow and MagicLinkRequestLimit bound how often one
	// customer can be sent a link. The limit protects the customer rather than
	// the server: without it, repeatedly asking for a link is a way to flood
	// somebody else's Telegram chat with sign-in prompts.
	MagicLinkRequestWindow = time.Hour
	MagicLinkRequestLimit  = 5

	// MagicLinkRetention is how long a consumed or expired row is kept before
	// the sweep removes it, so a customer's security list can still show that
	// the route was taken.
	MagicLinkRetention = 30 * 24 * time.Hour
)

// NewMagicLinkToken returns the token to deliver and the digest to store.
//
// It uses the same 256-bit construction as a session token because it is the
// same kind of secret: a bearer credential with no user-chosen entropy, guessing
// which must be infeasible regardless of any rate limit in front of it.
func NewMagicLinkToken() (token string, digest []byte, err error) { return websession.NewToken() }

// HashMagicLinkToken returns the storage digest for a delivered token.
func HashMagicLinkToken(token string) []byte { return websession.HashToken(token) }
