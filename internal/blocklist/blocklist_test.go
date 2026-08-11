package blocklist

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestNormalizeCollapsesTheEquivalencesThatMatter(t *testing.T) {
	cases := []struct {
		kind, input, want string
	}{
		{SubjectTelegramID, " 0012345 ", "12345"},
		{SubjectTelegramID, "#12345", "12345"},
		{SubjectUsername, "@Spammer", "spammer"},
		{SubjectUsername, "https://t.me/Spammer", "spammer"},
		{SubjectEmail, "Person@Example.COM", "Person@example.com"},
	}
	for _, testCase := range cases {
		got, err := Normalize(testCase.kind, testCase.input)
		if err != nil {
			t.Fatalf("Normalize(%q, %q): %v", testCase.kind, testCase.input, err)
		}
		if got != testCase.want {
			t.Fatalf("Normalize(%q, %q) = %q, want %q", testCase.kind, testCase.input, got, testCase.want)
		}
	}
}

func TestNormalizeRejectsWhatCannotIdentifyAnybody(t *testing.T) {
	for _, testCase := range []struct{ kind, input string }{
		{SubjectTelegramID, "not-a-number"},
		{SubjectTelegramID, "-5"},
		{SubjectTelegramID, "0"},
		{SubjectEmail, "no-at-sign"},
		{SubjectEmail, "trailing@"},
		{SubjectUsername, "   "},
	} {
		if _, err := Normalize(testCase.kind, testCase.input); err == nil {
			t.Fatalf("Normalize(%q, %q) accepted an unusable value", testCase.kind, testCase.input)
		}
	}

	if _, err := Normalize("passport", "x"); !errors.Is(err, ErrUnknownSubject) {
		t.Fatalf("expected ErrUnknownSubject for an unsupported kind, got %v", err)
	}
}

func TestFingerprintIsSaltedByKind(t *testing.T) {
	asID, err := Fingerprint(SubjectTelegramID, "12345")
	if err != nil {
		t.Fatal(err)
	}
	asUsername, err := Fingerprint(SubjectUsername, "12345")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(asID, asUsername) {
		t.Fatalf("a numeric username must not collide with a Telegram identifier: %s",
			hex.EncodeToString(asID))
	}
	if len(asID) != 32 {
		t.Fatalf("expected a 32-byte digest, got %d", len(asID))
	}

	// Normalisation happens before hashing, so equivalent spellings collide by
	// design — that is what makes an exact-match lookup work at all.
	spelled, err := Fingerprint(SubjectUsername, "@12345")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(asUsername, spelled) {
		t.Fatalf("equivalent usernames must share a fingerprint")
	}
}

func TestParseReadsPlainTextWithCommentsAndReasons(t *testing.T) {
	body := strings.NewReader(strings.Join([]string{
		"# published 2026-08-13",
		"",
		"111111",
		"222222,payment-fraud",
		"333333\tchargebacks",
		"not-a-number",
	}, "\n"))

	entries, skipped, err := Parse(SubjectTelegramID, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected three usable entries, got %d", len(entries))
	}
	if skipped != 1 {
		t.Fatalf("expected the malformed line to be counted as skipped, got %d", skipped)
	}
	if entries[1].ReasonCode != "payment-fraud" || entries[2].ReasonCode != "chargebacks" {
		t.Fatalf("reason codes were not carried through: %+v", entries)
	}
}

func TestParseReadsBothJSONShapes(t *testing.T) {
	body := strings.NewReader(`[
	  "444444",
	  {"value": "555555", "reason": "spam"},
	  {"id": "666666"},
	  {"unrelated": true}
	]`)

	entries, skipped, err := Parse(SubjectTelegramID, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected three usable entries, got %d", len(entries))
	}
	if skipped != 1 {
		t.Fatalf("expected the object with no identifier to be skipped, got %d", skipped)
	}
	if entries[1].ReasonCode != "spam" {
		t.Fatalf("expected the reason to survive, got %q", entries[1].ReasonCode)
	}
}

func TestParseSkipsLeadingWhitespaceWhenChoosingAShape(t *testing.T) {
	entries, _, err := Parse(SubjectUsername, strings.NewReader("\n\n  [\"@spammer\"]"))
	if err != nil {
		t.Fatalf("a JSON body with leading blank lines must still parse as JSON: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(entries))
	}
}

func TestParseRejectsAnImplausiblyLargeList(t *testing.T) {
	var body strings.Builder
	for index := 0; index <= MaxEntries+1; index++ {
		body.WriteString("1\n")
	}
	if _, _, err := Parse(SubjectTelegramID, strings.NewReader(body.String())); !errors.Is(err, ErrTooManyEntries) {
		t.Fatalf("expected ErrTooManyEntries, got %v", err)
	}
}

func TestParseRejectsAnUnknownKindBeforeReadingTheBody(t *testing.T) {
	if _, _, err := Parse("passport", strings.NewReader("1\n")); !errors.Is(err, ErrUnknownSubject) {
		t.Fatalf("expected ErrUnknownSubject, got %v", err)
	}
}

func TestParseHandlesAnEmptyBody(t *testing.T) {
	entries, skipped, err := Parse(SubjectTelegramID, strings.NewReader(""))
	if err != nil {
		t.Fatalf("an empty body is an empty list, not an error: %v", err)
	}
	if len(entries) != 0 || skipped != 0 {
		t.Fatalf("expected nothing from an empty body, got %d entries and %d skipped", len(entries), skipped)
	}
}
