package adminauth

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenerateRecoveryCodesAreDistinctAndWellFormed(t *testing.T) {
	codes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("got %d codes, want %d", len(codes), RecoveryCodeCount)
	}

	seen := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		if _, duplicate := seen[code]; duplicate {
			t.Fatalf("code %q was issued twice", code)
		}
		seen[code] = struct{}{}

		groups := strings.Split(code, "-")
		if len(groups) != recoveryCodeGroups {
			t.Fatalf("code %q has %d groups, want %d", code, len(groups), recoveryCodeGroups)
		}
		for _, group := range groups {
			if len(group) != recoveryGroupLength {
				t.Fatalf("code %q has a group of length %d, want %d", code, len(group), recoveryGroupLength)
			}
			for _, symbol := range group {
				if !strings.ContainsRune(recoveryAlphabet, symbol) {
					t.Fatalf("code %q contains %q, which is outside the alphabet", code, symbol)
				}
			}
		}
	}
}

// The alphabet deliberately omits the characters that are easy to confuse when
// a code is read off a screen and typed back in.
func TestRecoveryAlphabetOmitsAmbiguousCharacters(t *testing.T) {
	for _, symbol := range "ILOU" {
		if strings.ContainsRune(recoveryAlphabet, symbol) {
			t.Fatalf("alphabet contains the ambiguous character %q", symbol)
		}
	}
}

// An operator retyping a code from paper should not be defeated by case or by
// the presentational hyphen.
func TestHashRecoveryCodeIgnoresFormatting(t *testing.T) {
	codes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	code := codes[0]

	canonical := HashRecoveryCode(code)
	for _, variant := range []string{
		strings.ToLower(code),
		strings.ReplaceAll(code, "-", ""),
		" " + strings.ToLower(strings.ReplaceAll(code, "-", " ")) + " ",
	} {
		if !bytes.Equal(HashRecoveryCode(variant), canonical) {
			t.Fatalf("variant %q hashed differently from %q", variant, code)
		}
	}
}

func TestHashRecoveryCodeSeparatesDistinctCodes(t *testing.T) {
	codes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	if bytes.Equal(HashRecoveryCode(codes[0]), HashRecoveryCode(codes[1])) {
		t.Fatal("two distinct codes produced the same digest")
	}
	if length := len(HashRecoveryCode(codes[0])); length != 32 {
		t.Fatalf("digest is %d bytes, want 32", length)
	}
}

func TestNormalizeRecoveryCodeDropsUnknownSymbols(t *testing.T) {
	if got := NormalizeRecoveryCode("ab!cd-01 23"); got != "ABCD0123" {
		t.Fatalf("NormalizeRecoveryCode = %q, want %q", got, "ABCD0123")
	}
}
