package adminauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

// TOTP is implemented here rather than pulled from a dependency because RFC
// 6238 is small, fully specified, and verifiable against the published test
// vectors in the RFC itself — which totp_test.go asserts against. That gives
// stronger assurance for a security-critical path than an unaudited module.
//
// SHA-1 is the algorithm here because RFC 6238 specifies it and because every
// authenticator app implements it; the stronger variants are not universally
// supported. Its use in HMAC is not affected by the collision attacks that
// retired SHA-1 for signatures.

const (
	// TOTPDigits is the code length. Six is what authenticator apps assume.
	TOTPDigits = 6
	// TOTPPeriod is the time step from RFC 6238.
	TOTPPeriod = 30 * time.Second
	// TOTPSecretBytes is the shared-secret size. RFC 4226 requires at least
	// 128 bits and recommends 160, which matches the HMAC-SHA1 output.
	TOTPSecretBytes = 20
	// TOTPSkewSteps is how many steps either side of the current one are
	// accepted, tolerating roughly ±30s of clock drift between the server and
	// the authenticator. Widening this proportionally widens the window an
	// intercepted code stays usable in.
	TOTPSkewSteps = 1
)

// ErrInvalidTOTPSecret reports a secret that is not valid base32.
var ErrInvalidTOTPSecret = errors.New("TOTP secret is not valid base32")

// base32NoPadding is the unpadded, upper-case base32 alphabet authenticator
// apps expect in an otpauth URI.
var base32NoPadding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a new base32-encoded shared secret.
func GenerateTOTPSecret() (string, error) {
	secret := make([]byte, TOTPSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return base32NoPadding.EncodeToString(secret), nil
}

// TOTPCode computes the code for a specific time step counter.
func TOTPCode(secret string, counter uint64) (string, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return "", err
	}
	return hotp(key, counter, TOTPDigits), nil
}

// VerifyTOTP reports whether code is valid for the given moment, allowing
// TOTPSkewSteps of drift in each direction.
//
// The comparison against every candidate step is constant time and does not
// stop early, so the time taken does not reveal which step matched.
func VerifyTOTP(secret, code string, now time.Time) (bool, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return false, err
	}
	code = strings.TrimSpace(code)
	if len(code) != TOTPDigits {
		return false, nil
	}

	counter := totpCounter(now)
	matched := 0
	for offset := -TOTPSkewSteps; offset <= TOTPSkewSteps; offset++ {
		step, ok := shiftCounter(counter, offset)
		if !ok {
			continue
		}
		candidate := hotp(key, step, TOTPDigits)
		matched |= subtle.ConstantTimeCompare([]byte(candidate), []byte(code))
	}
	return matched == 1, nil
}

// TOTPURI builds the otpauth:// URI an authenticator app scans. The issuer is
// repeated as a parameter as well as a label prefix, which is what the Key URI
// format requires for correct display.
func TOTPURI(secret, issuer, account string) string {
	label := url.PathEscape(issuer + ":" + account)
	query := url.Values{}
	query.Set("secret", secret)
	query.Set("issuer", issuer)
	query.Set("algorithm", "SHA1")
	query.Set("digits", fmt.Sprintf("%d", TOTPDigits))
	query.Set("period", fmt.Sprintf("%d", int(TOTPPeriod.Seconds())))
	return "otpauth://totp/" + label + "?" + query.Encode()
}

// totpCounter converts a moment to its RFC 6238 time-step counter.
func totpCounter(now time.Time) uint64 {
	seconds := now.UTC().Unix()
	if seconds < 0 {
		return 0
	}
	return uint64(seconds) / uint64(TOTPPeriod.Seconds())
}

// shiftCounter offsets a counter without wrapping around zero, which would
// otherwise turn a small negative offset into an enormous counter.
func shiftCounter(counter uint64, offset int) (uint64, bool) {
	if offset < 0 {
		magnitude := uint64(-offset)
		if magnitude > counter {
			return 0, false
		}
		return counter - magnitude, true
	}
	return counter + uint64(offset), true
}

// hotp is the RFC 4226 truncation applied to HMAC-SHA1.
func hotp(key []byte, counter uint64, digits int) string {
	message := make([]byte, 8)
	binary.BigEndian.PutUint64(message, counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(message)
	sum := mac.Sum(nil)

	// Dynamic truncation: the low nibble of the last byte selects the offset of
	// the four-byte window, whose top bit is masked off to keep it positive.
	offset := sum[len(sum)-1] & 0x0f
	truncated := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff

	modulo := uint32(math.Pow10(digits))
	return fmt.Sprintf("%0*d", digits, truncated%modulo)
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	key, err := base32NoPadding.DecodeString(strings.TrimRight(normalized, "="))
	if err != nil || len(key) == 0 {
		return nil, ErrInvalidTOTPSecret
	}
	return key, nil
}
