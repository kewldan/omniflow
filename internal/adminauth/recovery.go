package adminauth

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"math/big"
	"strings"
)

const (
	// RecoveryCodeCount is how many codes are issued per enrolment. Replacing
	// the set always replaces all of them.
	RecoveryCodeCount = 10
	// recoveryCodeGroups and recoveryGroupLength give 10 characters from a
	// 32-symbol alphabet, i.e. 50 bits of entropy per code. That is far beyond
	// what the login rate limiter would ever allow an attacker to search.
	recoveryCodeGroups  = 2
	recoveryGroupLength = 5

	// recoveryAlphabet is Crockford base32 without I, L, O, and U, so a code
	// read off a screen and typed by hand cannot be ambiguous.
	recoveryAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

// ErrNoRecoveryCodes reports that an account has no unused codes left.
var ErrNoRecoveryCodes = errors.New("no unused recovery codes remain")

// GenerateRecoveryCodes returns a fresh set of formatted codes.
//
// The plaintext is returned exactly once, to be shown to the operator. Only the
// digests from HashRecoveryCode are stored, so a database read never yields a
// usable second factor.
func GenerateRecoveryCodes() ([]string, error) {
	codes := make([]string, 0, RecoveryCodeCount)
	for range RecoveryCodeCount {
		code, err := generateRecoveryCode()
		if err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, nil
}

// HashRecoveryCode returns the storage digest for a code.
//
// A plain SHA-256 is correct here, unlike for a password: these codes are
// generated with 50 bits of uniform entropy, so there is no dictionary for a
// slow hash to defend against.
func HashRecoveryCode(code string) []byte {
	sum := sha256.Sum256([]byte(NormalizeRecoveryCode(code)))
	return sum[:]
}

// NormalizeRecoveryCode makes matching insensitive to case, spacing, and the
// presentational separator, so an operator retyping a code from paper is not
// defeated by formatting.
func NormalizeRecoveryCode(code string) string {
	var builder strings.Builder
	builder.Grow(recoveryCodeGroups * recoveryGroupLength)
	for _, symbol := range strings.ToUpper(code) {
		if strings.ContainsRune(recoveryAlphabet, symbol) {
			builder.WriteRune(symbol)
		}
	}
	return builder.String()
}

func generateRecoveryCode() (string, error) {
	groups := make([]string, 0, recoveryCodeGroups)
	limit := big.NewInt(int64(len(recoveryAlphabet)))
	for range recoveryCodeGroups {
		var group strings.Builder
		for range recoveryGroupLength {
			// crypto/rand's Int is rejection-sampled, so the alphabet stays
			// uniform rather than skewed by a modulo bias.
			index, err := rand.Int(rand.Reader, limit)
			if err != nil {
				return "", err
			}
			group.WriteByte(recoveryAlphabet[index.Int64()])
		}
		groups = append(groups, group.String())
	}
	return strings.Join(groups, "-"), nil
}
