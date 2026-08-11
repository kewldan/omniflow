package adminauth

import (
	"errors"
	"strings"
	"testing"
)

// testParams keep argon2 cheap so the suite stays fast. Production callers pass
// DefaultPasswordParams.
var testParams = PasswordParams{
	Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
}

func TestHashPasswordRoundTrips(t *testing.T) {
	encoded, err := HashPassword("correct horse battery", testParams)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$") {
		t.Fatalf("hash is not PHC-encoded argon2id: %q", encoded)
	}

	ok, err := VerifyPassword("correct horse battery", encoded)
	if err != nil || !ok {
		t.Fatalf("VerifyPassword(correct) = %v, %v", ok, err)
	}

	ok, err = VerifyPassword("correct horse batterz", encoded)
	if err != nil {
		t.Fatalf("VerifyPassword(wrong) errored: %v", err)
	}
	if ok {
		t.Fatal("a wrong password verified")
	}
}

func TestHashPasswordUsesAFreshSalt(t *testing.T) {
	first, err := HashPassword("correct horse battery", testParams)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword("correct horse battery", testParams)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if first == second {
		t.Fatal("two hashes of the same password are identical, so the salt is not random")
	}
}

func TestValidatePasswordEnforcesLength(t *testing.T) {
	if err := ValidatePassword("short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("expected ErrPasswordTooShort, got %v", err)
	}
	if err := ValidatePassword(strings.Repeat("a", MaxPasswordLength+1)); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("expected ErrPasswordTooLong, got %v", err)
	}
	if err := ValidatePassword(strings.Repeat("a", MinPasswordLength)); err != nil {
		t.Fatalf("a minimum-length password was rejected: %v", err)
	}
}

// A passphrase of non-Latin characters is long enough in runes but not in
// bytes; counting bytes would reject it for the wrong reason.
func TestValidatePasswordCountsRunesNotBytes(t *testing.T) {
	if err := ValidatePassword("пароль-достаточной-длины"); err != nil {
		t.Fatalf("a Cyrillic passphrase was rejected: %v", err)
	}
}

func TestHashPasswordRejectsWeakInput(t *testing.T) {
	if _, err := HashPassword("short", testParams); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("expected ErrPasswordTooShort, got %v", err)
	}
}

// A damaged or truncated hash must fail closed rather than authenticate.
func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	valid, err := HashPassword("correct horse battery", testParams)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	malformed := []string{
		"",
		"not-a-hash",
		"$argon2i$v=19$m=8192,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=99$m=8192,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=8192,t=1,p=1$!!!!$aGFzaA",
		"$argon2id$v=19$m=8192,t=1,p=1$c2FsdA$",
		valid[:len(valid)-8],
	}
	for _, encoded := range malformed {
		ok, err := VerifyPassword("correct horse battery", encoded)
		if ok {
			t.Fatalf("malformed hash %q verified", encoded)
		}
		if err == nil && encoded != valid[:len(valid)-8] {
			t.Fatalf("malformed hash %q returned no error", encoded)
		}
	}
}

func TestNeedsRehashTracksPolicy(t *testing.T) {
	encoded, err := HashPassword("correct horse battery", testParams)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if NeedsRehash(encoded, testParams) {
		t.Fatal("a hash at current parameters was flagged for rehashing")
	}

	stronger := testParams
	stronger.Memory *= 4
	if !NeedsRehash(encoded, stronger) {
		t.Fatal("a hash below the raised memory cost was not flagged")
	}

	if !NeedsRehash("not-a-hash", testParams) {
		t.Fatal("an unparsable hash must always be replaced")
	}
}
