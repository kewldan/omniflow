// Package gifts holds the claim-code format and the redemption rules for a
// subscription, add-on, or wallet credit bought for somebody else.
//
// The rules live here, away from the database and the bot, because redemption
// is the part of a gift that has to be exactly once. A code that can be
// redeemed twice hands out two entitlements for one payment; a code that can be
// guessed hands one out for none. Both are decided by the arithmetic in this
// file, so both are provable in a unit test.
//
// The database is what actually enforces single redemption — the claim update
// only matches a deliverable, unexpired row — and this package decides what to
// tell the person holding the code when it does not.
package gifts

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strings"
	"time"
)

// Gift statuses match the database check constraint on `gifts.status`.
const (
	StatusPending     = "pending"
	StatusDeliverable = "deliverable"
	StatusClaimed     = "claimed"
	StatusExpired     = "expired"
	StatusRevoked     = "revoked"
	StatusRefunded    = "refunded"
)

// Gift kinds match the database check constraint on `gifts.kind`.
const (
	KindSubscription = "subscription"
	KindAddon        = "addon"
	KindCredit       = "wallet_credit"
)

// CodeLength is the number of characters in a claim code.
//
// Sixteen Crockford characters carry 80 bits. Combined with the attempt ceiling
// below and the rate limit in front of the endpoint, guessing one is not a
// realistic attack even against an installation with a large number of live
// gifts.
const CodeLength = 16

// MaxClaimAttempts bounds how many times one code may be presented and refused.
//
// It is durable state on the gift row rather than a counter in Valkey, so the
// ceiling survives a restart and cannot be reset by waiting out a rate-limit
// window.
const MaxClaimAttempts = 10

// alphabet is Crockford base32: no I, L, O, or U, so a hand-copied code has no
// visually ambiguous character in it and no accidental word in it either.
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	// ErrInvalidCode reports a code that cannot be a claim code at all.
	ErrInvalidCode = errors.New("gift code is not valid")
	// ErrCodeGeneration reports a failure to read the system entropy source.
	ErrCodeGeneration = errors.New("gift code could not be generated")
)

// Rejection explains why a claim was refused.
//
// These are stable identifiers rather than sentences: the bot and the panel
// each render their own localised copy, and the value appears in audit
// metadata.
type Rejection string

const (
	RejectionNone            Rejection = ""
	RejectionUnknownCode     Rejection = "unknown_code"
	RejectionNotSettled      Rejection = "not_settled"
	RejectionAlreadyClaimed  Rejection = "already_claimed"
	RejectionExpired         Rejection = "expired"
	RejectionRevoked         Rejection = "revoked"
	RejectionSelfClaim       Rejection = "self_claim"
	RejectionWrongRecipient  Rejection = "wrong_recipient"
	RejectionTooManyAttempts Rejection = "too_many_attempts"
)

// NewCode returns a fresh claim code and the four-character hint stored beside
// its digest.
//
// The hint is what lets a sender tell two of their own gifts apart in a list.
// Four characters out of eighty bits leaves the code itself unguessable from
// the hint, which is why the hint may be shown and the code may not.
func NewCode() (code string, hint string, err error) {
	buffer := make([]byte, CodeLength)
	if _, readErr := rand.Read(buffer); readErr != nil {
		return "", "", ErrCodeGeneration
	}

	builder := strings.Builder{}
	builder.Grow(CodeLength)
	for _, value := range buffer {
		// The alphabet is exactly 32 characters, so masking the low five bits
		// selects uniformly with no modulo bias to correct for.
		builder.WriteByte(alphabet[value&0x1f])
	}
	code = builder.String()
	return code, code[CodeLength-4:], nil
}

// Normalize turns what somebody typed into the canonical code.
//
// Separators are stripped because a code is easier to read in groups, and the
// three Crockford substitutions are applied because a person copying by hand
// reliably confuses those characters. Anything else is rejected rather than
// guessed at.
func Normalize(input string) (string, error) {
	upper := strings.ToUpper(strings.TrimSpace(input))
	builder := strings.Builder{}
	builder.Grow(len(upper))

	for _, character := range upper {
		switch character {
		case ' ', '-', '_':
			continue
		case 'I', 'L':
			builder.WriteRune('1')
		case 'O':
			builder.WriteRune('0')
		case 'U':
			// Crockford excludes U deliberately to avoid accidental words; there
			// is no sensible character to map it to.
			return "", ErrInvalidCode
		default:
			if !strings.ContainsRune(alphabet, character) {
				return "", ErrInvalidCode
			}
			builder.WriteRune(character)
		}
	}

	normalized := builder.String()
	if len(normalized) != CodeLength {
		return "", ErrInvalidCode
	}
	return normalized, nil
}

// Hash returns the digest stored for a code. The plaintext never reaches the
// database, so a dump of the gifts table yields nothing redeemable.
func Hash(code string) []byte {
	digest := sha256.Sum256([]byte(code))
	return digest[:]
}

// State is the stored gift as the claim rules see it.
type State struct {
	Status              string
	ExpiresAt           time.Time
	ClaimAttempts       int
	SenderUserID        string
	RecipientTelegramID *int64
}

// Claimant is the person presenting a code.
type Claimant struct {
	UserID     string
	TelegramID *int64
}

// EvaluateClaim decides whether a code may be redeemed right now.
//
// The order of the checks is deliberate. The attempt ceiling comes first, so a
// gift being hammered stops answering anything at all rather than continuing to
// leak which failure it is. Revocation and expiry come before the recipient
// check, because telling somebody "that is not your gift" about a code that is
// already dead reveals more than it needs to.
func EvaluateClaim(state State, claimant Claimant, now time.Time) Rejection {
	if state.ClaimAttempts >= MaxClaimAttempts {
		return RejectionTooManyAttempts
	}

	switch state.Status {
	case StatusPending:
		// The sender's own payment has not settled yet. A gift bought a second
		// ago is a real gift, so this is a "not yet", not a refusal.
		return RejectionNotSettled
	case StatusClaimed:
		return RejectionAlreadyClaimed
	case StatusRevoked, StatusRefunded:
		return RejectionRevoked
	case StatusExpired:
		return RejectionExpired
	case StatusDeliverable:
		// Fall through to the time and recipient checks.
	default:
		return RejectionUnknownCode
	}

	if !state.ExpiresAt.After(now) {
		// The sweeper has not run yet, but the gift is over regardless.
		return RejectionExpired
	}
	if state.SenderUserID != "" && state.SenderUserID == claimant.UserID {
		// Redeeming your own gift would let a customer route money through the
		// gift path to reach a promotion or reward they are not eligible for.
		return RejectionSelfClaim
	}
	if state.RecipientTelegramID != nil {
		if claimant.TelegramID == nil || *claimant.TelegramID != *state.RecipientTelegramID {
			return RejectionWrongRecipient
		}
	}
	return RejectionNone
}

// CanRevoke reports whether an operator may reclaim a gift.
//
// A claimed gift is deliberately not revocable: the recipient already holds
// what it bought, and taking that back is a refund decision made against the
// entitlement or the ledger, with its own record and its own permission.
func CanRevoke(status string) bool {
	switch status {
	case StatusPending, StatusDeliverable, StatusExpired:
		return true
	default:
		return false
	}
}

// RefundEligible reports whether the sender's payment may be returned.
//
// Only a gift that was never redeemed qualifies. Refunding a claimed gift would
// mean the sender's money back and the recipient's entitlement intact, which is
// the shape of the abuse this rule exists to prevent.
func RefundEligible(status string) bool {
	return status == StatusRevoked || status == StatusExpired
}
