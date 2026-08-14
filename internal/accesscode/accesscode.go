// Package accesscode holds the one code format Omniflow issues.
//
// A gift code and a wholesale batch code are the same thing to the person
// holding one: sixteen characters that turn into a subscription. They were
// going to be the same format anyway, so they are the same code — which is what
// lets one redemption box accept either, and means a customer never has to know
// which kind of code somebody handed them.
//
// The properties that matter are all here, so all of them are provable in a
// unit test rather than spread across two packages that could drift:
//
//   - Eighty bits of entropy, read from the system source, with no modulo bias.
//   - Crockford base32, so a hand-copied code contains no visually ambiguous
//     character and no accidental word.
//   - Only the SHA-256 is ever stored. A dump of either table yields nothing
//     redeemable.
package accesscode

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strings"
)

// Length is the number of characters in a code.
//
// Sixteen Crockford characters carry 80 bits. Combined with a per-code attempt
// ceiling and the rate limit in front of the redemption endpoint, guessing one
// is not a realistic attack even against an installation holding a large number
// of live codes.
const Length = 16

// Alphabet is Crockford base32: no I, L, O, or U, so a hand-copied code has no
// visually ambiguous character in it and no accidental word in it either.
const Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	// ErrInvalid reports a value that cannot be a code at all.
	ErrInvalid = errors.New("code is not valid")
	// ErrGeneration reports a failure to read the system entropy source.
	ErrGeneration = errors.New("code could not be generated")
)

// New returns a fresh code and the four-character hint stored beside its digest.
//
// The hint is what lets an operator match a support question to a row without
// holding anything redeemable. Four characters out of eighty bits leaves the
// code itself unguessable from the hint, which is why the hint may be shown and
// the code may not.
func New() (code string, hint string, err error) {
	buffer := make([]byte, Length)
	if _, readErr := rand.Read(buffer); readErr != nil {
		return "", "", ErrGeneration
	}

	builder := strings.Builder{}
	builder.Grow(Length)
	for _, value := range buffer {
		// The alphabet is exactly 32 characters, so masking the low five bits
		// selects uniformly with no modulo bias to correct for.
		builder.WriteByte(Alphabet[value&0x1f])
	}
	code = builder.String()
	return code, code[Length-4:], nil
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
			return "", ErrInvalid
		default:
			if !strings.ContainsRune(Alphabet, character) {
				return "", ErrInvalid
			}
			builder.WriteRune(character)
		}
	}

	normalized := builder.String()
	if len(normalized) != Length {
		return "", ErrInvalid
	}
	return normalized, nil
}

// Hash returns the digest stored for a code. The plaintext never reaches the
// database.
func Hash(code string) []byte {
	digest := sha256.Sum256([]byte(code))
	return digest[:]
}
