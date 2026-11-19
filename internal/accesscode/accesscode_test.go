package accesscode

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestNewIssuesCrockfordCodesWithTheirOwnHint(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{}, 512)
	counts := make(map[rune]int, len(Alphabet))
	for range 512 {
		code, hint, err := New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if len(code) != Length {
			t.Fatalf("code %q has %d characters, want %d", code, len(code), Length)
		}
		for _, character := range code {
			if !strings.ContainsRune(Alphabet, character) {
				t.Fatalf("code %q contains %q, which is outside the Crockford alphabet", code, character)
			}
			counts[character]++
		}
		if hint != code[Length-4:] {
			t.Fatalf("hint %q is not the last four characters of %q", hint, code)
		}
		// A freshly issued code must already be canonical, or the code printed
		// for a customer would not redeem as typed.
		normalized, err := Normalize(code)
		if err != nil || normalized != code {
			t.Fatalf("Normalize(%q) = %q, %v; a fresh code must be canonical", code, normalized, err)
		}
		if _, dup := seen[code]; dup {
			t.Fatalf("New repeated %q", code)
		}
		seen[code] = struct{}{}
	}
	// 8192 characters over 32 symbols averages 256 each. A symbol that never
	// appears means the mask or the alphabet is wrong, not bad luck.
	for _, character := range Alphabet {
		if counts[character] == 0 {
			t.Fatalf("symbol %q never appeared in 8192 generated characters", character)
		}
	}
}

func TestAlphabetIsCrockford(t *testing.T) {
	t.Parallel()
	if len(Alphabet) != 32 {
		t.Fatalf("alphabet has %d symbols; the five-bit mask needs exactly 32", len(Alphabet))
	}
	for _, excluded := range "ILOU" {
		if strings.ContainsRune(Alphabet, excluded) {
			t.Fatalf("alphabet contains the ambiguous %q", excluded)
		}
	}
}

func TestNormalizeAcceptsWhatPeopleTypeAndRefusesTheRest(t *testing.T) {
	t.Parallel()
	const canonical = "0123456789ABCDEF"
	cases := []struct {
		name  string
		input string
		want  string
		err   error
	}{
		{"canonical", canonical, canonical, nil},
		{"lower case", strings.ToLower(canonical), canonical, nil},
		{"grouped with dashes", "0123-4567-89AB-CDEF", canonical, nil},
		{"grouped with spaces", " 0123 4567 89ab cdef ", canonical, nil},
		{"grouped with underscores", "0123_4567_89AB_CDEF", canonical, nil},
		{"surrounding whitespace", "\t" + canonical + "\n", canonical, nil},
		{"I and L read as one", "I1L1il0123456789", "1111110123456789", nil},
		{"O read as zero", "OoOo456789ABCDEF", "0000456789ABCDEF", nil},
		{"U refused", "U123456789ABCDEF", "", ErrInvalid},
		{"punctuation refused", "0123.4567.89AB.CDEF", "", ErrInvalid},
		{"one short", canonical[:Length-1], "", ErrInvalid},
		{"one long", canonical + "0", "", ErrInvalid},
		{"empty", "", "", ErrInvalid},
		{"only separators", "----", "", ErrInvalid},
		{"non-ASCII digit refused", "٠123456789ABCDEF", "", ErrInvalid},
		{"cyrillic look-alike refused", "А123456789ABCDEF", "", ErrInvalid},
		{"interior newline refused", "01234567\n89ABCDEF", "", ErrInvalid},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, err := Normalize(testCase.input)
			if !errors.Is(err, testCase.err) {
				t.Fatalf("Normalize(%q) error = %v, want %v", testCase.input, err, testCase.err)
			}
			if got != testCase.want {
				t.Fatalf("Normalize(%q) = %q, want %q", testCase.input, got, testCase.want)
			}
		})
	}
}

func TestHashIsTheOnlyThingStored(t *testing.T) {
	t.Parallel()
	code, _, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	digest := Hash(code)
	if len(digest) != 32 {
		t.Fatalf("digest length = %d; the schema requires 32", len(digest))
	}
	if !bytes.Equal(digest, Hash(code)) {
		t.Fatal("the same code hashed to two digests; a code could never be redeemed")
	}
	if bytes.Contains(digest, []byte(code)) {
		t.Fatal("the digest carries the code")
	}
	// Redemption hashes the normalized input, so every spelling a customer may
	// type has to land on the digest that was stored.
	spelled := strings.ToLower(code[:4]) + "-" + code[4:8] + " " + code[8:]
	normalized, err := Normalize(spelled)
	if err != nil {
		t.Fatalf("Normalize(%q): %v", spelled, err)
	}
	if !bytes.Equal(Hash(normalized), digest) {
		t.Fatal("a grouped, lower-case spelling of the code does not redeem")
	}
}
