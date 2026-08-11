package adminauth

import (
	"strings"
	"testing"
	"time"
)

// rfc6238Secret is the ASCII string "12345678901234567890" from RFC 6238
// Appendix B, base32-encoded as an authenticator app would carry it.
const rfc6238Secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

// TestTOTPMatchesRFC6238Vectors checks the implementation against the codes
// published in RFC 6238 Appendix B for HMAC-SHA1. These are the authoritative
// vectors: passing them means an off-the-shelf authenticator app will agree
// with this server.
func TestTOTPMatchesRFC6238Vectors(t *testing.T) {
	key, err := decodeTOTPSecret(rfc6238Secret)
	if err != nil {
		t.Fatalf("decodeTOTPSecret: %v", err)
	}

	// The RFC tabulates 8-digit codes against Unix timestamps.
	vectors := []struct {
		unix int64
		code string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}

	for _, vector := range vectors {
		counter := totpCounter(time.Unix(vector.unix, 0).UTC())
		if got := hotp(key, counter, 8); got != vector.code {
			t.Fatalf("T=%d: hotp = %s, want %s", vector.unix, got, vector.code)
		}
	}
}

func TestVerifyTOTPAcceptsTheCurrentCode(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()

	code, err := TOTPCode(secret, totpCounter(now))
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	if len(code) != TOTPDigits {
		t.Fatalf("code %q is not %d digits", code, TOTPDigits)
	}

	ok, err := VerifyTOTP(secret, code, now)
	if err != nil || !ok {
		t.Fatalf("VerifyTOTP(current) = %v, %v", ok, err)
	}
}

// Drift of one step in either direction is tolerated; two is not. This is the
// boundary that decides how long an intercepted code stays usable.
func TestVerifyTOTPToleratesExactlyOneStepOfDrift(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	code, err := TOTPCode(secret, totpCounter(now))
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}

	for _, offset := range []time.Duration{-TOTPPeriod, 0, TOTPPeriod} {
		ok, err := VerifyTOTP(secret, code, now.Add(offset))
		if err != nil || !ok {
			t.Fatalf("offset %s: VerifyTOTP = %v, %v; want accepted", offset, ok, err)
		}
	}
	for _, offset := range []time.Duration{-2 * TOTPPeriod, 2 * TOTPPeriod} {
		ok, err := VerifyTOTP(secret, code, now.Add(offset))
		if err != nil {
			t.Fatalf("offset %s errored: %v", offset, err)
		}
		if ok {
			t.Fatalf("offset %s was accepted; the skew window is too wide", offset)
		}
	}
}

func TestVerifyTOTPRejectsMalformedInput(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()

	for _, code := range []string{"", "12345", "1234567", "abcdef"} {
		ok, err := VerifyTOTP(secret, code, now)
		if err != nil {
			t.Fatalf("code %q errored: %v", code, err)
		}
		if ok {
			t.Fatalf("malformed code %q was accepted", code)
		}
	}

	if _, err := VerifyTOTP("not base32!", "123456", now); err == nil {
		t.Fatal("an invalid secret returned no error")
	}
}

// A counter near the epoch must not wrap when the negative skew step is
// applied, which would otherwise probe an enormous counter instead.
func TestVerifyTOTPHandlesTheEpochBoundary(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	code, err := TOTPCode(secret, 0)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	ok, err := VerifyTOTP(secret, code, time.Unix(0, 0).UTC())
	if err != nil || !ok {
		t.Fatalf("VerifyTOTP at the epoch = %v, %v", ok, err)
	}
}

func TestGenerateTOTPSecretIsUniqueAndDecodable(t *testing.T) {
	seen := make(map[string]struct{}, 32)
	for range 32 {
		secret, err := GenerateTOTPSecret()
		if err != nil {
			t.Fatalf("GenerateTOTPSecret: %v", err)
		}
		if _, duplicate := seen[secret]; duplicate {
			t.Fatal("GenerateTOTPSecret repeated a secret")
		}
		seen[secret] = struct{}{}

		key, err := decodeTOTPSecret(secret)
		if err != nil {
			t.Fatalf("decodeTOTPSecret: %v", err)
		}
		if len(key) != TOTPSecretBytes {
			t.Fatalf("secret decoded to %d bytes, want %d", len(key), TOTPSecretBytes)
		}
	}
}

func TestTOTPURICarriesTheEnrolmentParameters(t *testing.T) {
	uri := TOTPURI(rfc6238Secret, "Omniflow", "owner@example.com")
	for _, fragment := range []string{
		"otpauth://totp/",
		"secret=" + rfc6238Secret,
		"issuer=Omniflow",
		"algorithm=SHA1",
		"digits=6",
		"period=30",
	} {
		if !strings.Contains(uri, fragment) {
			t.Fatalf("URI %q is missing %q", uri, fragment)
		}
	}
}
