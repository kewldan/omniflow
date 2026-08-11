package adminauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// PasswordParams are the argon2id cost parameters used to hash a new password.
//
// The defaults exceed the OWASP argon2id minimum (19 MiB / t=2 / p=1). Memory
// is the parameter that actually costs an attacker with parallel hardware, so
// it is raised well above the floor while iterations stay low enough that a
// sign-in remains responsive.
//
// Every hash records the parameters it was produced with, so raising these
// values needs no migration: existing hashes keep verifying under their own
// recorded costs and are re-hashed in place on the next successful sign-in.
type PasswordParams struct {
	// Memory is the argon2 memory cost in KiB.
	Memory uint32
	// Iterations is the argon2 time cost.
	Iterations uint32
	// Parallelism is the number of lanes.
	Parallelism uint8
	// SaltLength and KeyLength are in bytes.
	SaltLength uint32
	KeyLength  uint32
}

// DefaultPasswordParams is the current recommended configuration.
var DefaultPasswordParams = PasswordParams{
	Memory:      64 * 1024,
	Iterations:  3,
	Parallelism: 2,
	SaltLength:  16,
	KeyLength:   32,
}

const (
	// MinPasswordLength follows NIST SP 800-63B: length is the control that
	// matters, and composition rules are deliberately not imposed.
	MinPasswordLength = 12
	// MaxPasswordLength bounds the work a single unauthenticated request can
	// ask argon2 to do.
	MaxPasswordLength = 256
)

var (
	// ErrPasswordTooShort and ErrPasswordTooLong are surfaced to the operator.
	ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	ErrPasswordTooLong  = fmt.Errorf("password must be at most %d characters", MaxPasswordLength)

	// ErrInvalidPasswordHash means the stored hash could not be parsed. It is
	// never shown to a caller: an unparsable hash is treated as a failed
	// verification so a corrupted row cannot become an authentication bypass.
	ErrInvalidPasswordHash = errors.New("stored password hash is malformed")
)

// ValidatePassword enforces the length policy on a new password.
func ValidatePassword(password string) error {
	// Counting runes rather than bytes keeps a non-Latin passphrase from being
	// rejected for being "too short" when it is not.
	length := utf8.RuneCountInString(password)
	switch {
	case length < MinPasswordLength:
		return ErrPasswordTooShort
	case length > MaxPasswordLength:
		return ErrPasswordTooLong
	}
	return nil
}

// HashPassword derives a PHC-encoded argon2id hash with the given parameters.
// Pass DefaultPasswordParams unless a test needs cheaper costs.
func HashPassword(password string, params PasswordParams) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey(
		[]byte(password), salt,
		params.Iterations, params.Memory, params.Parallelism, params.KeyLength,
	)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, params.Memory, params.Iterations, params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether the password matches the encoded hash.
//
// The comparison is constant time. A malformed hash returns false with an
// error rather than panicking, so a damaged row fails closed.
func VerifyPassword(password, encoded string) (bool, error) {
	params, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey(
		[]byte(password), salt,
		params.Iterations, params.Memory, params.Parallelism, uint32(len(want)),
	)
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NeedsRehash reports whether a stored hash was produced with weaker
// parameters than the current policy. Callers re-hash on the next successful
// sign-in, which is the only moment the plaintext is available.
func NeedsRehash(encoded string, params PasswordParams) bool {
	stored, _, key, err := decodeHash(encoded)
	if err != nil {
		// An unparsable hash cannot be verified against, so replacing it is
		// always the right move once the operator proves the password.
		return true
	}
	return stored.Memory < params.Memory ||
		stored.Iterations < params.Iterations ||
		stored.Parallelism < params.Parallelism ||
		uint32(len(key)) < params.KeyLength
}

// decodeHash parses the PHC string produced by HashPassword.
func decodeHash(encoded string) (PasswordParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// A PHC string starts with an empty segment because it begins with "$".
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}
	if version != argon2.Version {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}

	var params PasswordParams
	if _, err := fmt.Sscanf(
		parts[3], "m=%d,t=%d,p=%d", &params.Memory, &params.Iterations, &params.Parallelism,
	); err != nil {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}
	if len(salt) == 0 || len(key) == 0 {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}

	params.SaltLength = uint32(len(salt))
	params.KeyLength = uint32(len(key))
	return params, salt, key, nil
}
