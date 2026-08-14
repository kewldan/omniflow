package adminauth

import (
	"errors"
	"strings"
	"testing"
)

// The relying party is derived from the public URL rather than configured
// beside it, so the two cannot disagree — a disagreement the browser reports
// only by refusing to make a credential at all.
func TestRelyingPartyIsDerivedFromThePublicURL(t *testing.T) {
	for _, testCase := range []struct {
		name, publicURL, id, origin string
	}{
		{"https", "https://panel.example.com", "panel.example.com", "https://panel.example.com"},
		{"a path is ignored", "https://panel.example.com/admin", "panel.example.com", "https://panel.example.com"},
		{
			// The port stays in the origin and out of the identifier: the
			// browser reports the origin with its port, and a credential is
			// scoped to the domain regardless of it.
			"a development port", "http://localhost:3000", "localhost", "http://localhost:3000",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			party, err := NewRelyingParty(testCase.publicURL, "Omniflow")
			if err != nil {
				t.Fatalf("derive: %v", err)
			}
			if party.ID != testCase.id {
				t.Fatalf("ID = %q, want %q", party.ID, testCase.id)
			}
			if party.Origin != testCase.origin {
				t.Fatalf("Origin = %q, want %q", party.Origin, testCase.origin)
			}
		})
	}
}

// Without an absolute public URL there is nothing to bind a credential to, and
// the right answer is to refuse rather than to guess at a hostname.
func TestRelyingPartyRefusesWhatItCannotDerive(t *testing.T) {
	for _, publicURL := range []string{"", "   ", "panel.example.com", "/admin", "ftp://panel"} {
		if _, err := NewRelyingParty(publicURL, "Omniflow"); !errors.Is(err, ErrPasskeyOriginUnknown) {
			t.Fatalf("NewRelyingParty(%q) err = %v, want ErrPasskeyOriginUnknown", publicURL, err)
		}
	}
}

// The counter is the only clone detection a passkey has, and the common
// hardware does not implement it. Both facts have to hold at once.
func TestCheckSignCount(t *testing.T) {
	for _, testCase := range []struct {
		name              string
		stored, presented uint32
		wantErr           bool
	}{
		{name: "an authenticator that counts", stored: 4, presented: 5},
		{name: "the first assertion of a counting authenticator", stored: 0, presented: 1},
		// Most platform authenticators report zero forever. Refusing them would
		// lock out the majority of the hardware people actually have.
		{name: "an authenticator that never counts", stored: 0, presented: 0},
		{name: "a repeated value", stored: 7, presented: 7, wantErr: true},
		{name: "a counter that went backwards", stored: 7, presented: 3, wantErr: true},
		// A counting authenticator that suddenly reports zero is the clone
		// signature, not a non-counting one: it counted before.
		{name: "a counter that reset", stored: 7, presented: 0, wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := CheckSignCount(testCase.stored, testCase.presented)
			if testCase.wantErr != (err != nil) {
				t.Fatalf("CheckSignCount(%d, %d) = %v", testCase.stored, testCase.presented, err)
			}
			if testCase.wantErr && !errors.Is(err, ErrPasskeyCloned) {
				t.Fatalf("err = %v, want ErrPasskeyCloned", err)
			}
		})
	}
}

// A label is what an operator reads when deciding which key to revoke, so it is
// never empty and never longer than the column allows.
func TestPasskeyLabel(t *testing.T) {
	if got := PasskeyLabel("  Work laptop  ", "iPhone"); got != "Work laptop" {
		t.Fatalf("label = %q", got)
	}
	if got := PasskeyLabel("", "iPhone"); got != "iPhone" {
		t.Fatalf("fallback = %q", got)
	}
	if got := PasskeyLabel("", ""); got != "Passkey" {
		t.Fatalf("last resort = %q", got)
	}
	long := PasskeyLabel(strings.Repeat("k", PasskeyLabelLimit+40), "")
	if len(long) > PasskeyLabelLimit {
		t.Fatalf("label of %d characters exceeds the column", len(long))
	}
}
