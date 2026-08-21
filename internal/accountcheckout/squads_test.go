package accountcheckout

import (
	"errors"
	"fmt"
	"testing"

	"github.com/omniflow/omniflow/internal/commerce"
)

func squadErr(reason string) error {
	return fmt.Errorf("%w: %s", commerce.ErrSquadSelection, reason)
}

// Only "not chosen yet" is incomplete. A selection the plan can never accept is
// an error, and so is anything unrelated to squads.
func TestSquadSelectionIncompleteRecognisesOnlyAnUnfinishedChoice(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err        error
		wantReason string
		wantOK     bool
	}{
		{nil, "", false},
		{squadErr(commerce.SquadSelectionRequired), commerce.SquadSelectionRequired, true},
		{squadErr(commerce.SquadSelectionTooFew), commerce.SquadSelectionTooFew, true},
		{squadErr(commerce.SquadSelectionTooMany), "", false},
		{squadErr(commerce.SquadSelectionUnknown), "", false},
		{squadErr(commerce.SquadSelectionRefused), "", false},
		{errors.New("database is down"), "", false},
		{fmt.Errorf("wrapped: %w", squadErr(commerce.SquadSelectionRequired)), commerce.SquadSelectionRequired, true},
	}
	for _, testCase := range cases {
		reason, ok := SquadSelectionIncomplete(testCase.err)
		if reason != testCase.wantReason || ok != testCase.wantOK {
			t.Errorf("SquadSelectionIncomplete(%v) = (%q, %v), want (%q, %v)",
				testCase.err, reason, ok, testCase.wantReason, testCase.wantOK)
		}
	}
}

// An edit is refused only when it could never become valid; the intermediate
// states a customer passes through on the way to a valid set are stored.
func TestValidateSquadEditRefusesOnlyTheUnrepairable(t *testing.T) {
	t.Parallel()
	two := 2
	required := SquadOffer{
		Selection: "required", Minimum: 2, Maximum: &two,
		Offered: []SelectableSquad{{SquadID: "a"}, {SquadID: "b"}, {SquadID: "c"}},
	}
	optional := SquadOffer{
		Selection: "optional", Minimum: 0,
		Offered: []SelectableSquad{{SquadID: "a"}, {SquadID: "b"}},
	}
	automatic := SquadOffer{Selection: "automatic"}
	unset := SquadOffer{}

	cases := []struct {
		name     string
		offer    SquadOffer
		selected []string
		wantErr  string
	}{
		{"required: nothing yet is stored", required, nil, ""},
		{"required: one of two is stored", required, []string{"a"}, ""},
		{"required: exactly enough", required, []string{"a", "b"}, ""},
		{"required: too many is refused", required, []string{"a", "b", "c"}, commerce.SquadSelectionTooMany},
		{"required: unknown squad is refused", required, []string{"a", "zzz"}, commerce.SquadSelectionUnknown},
		{"optional: empty is fine", optional, nil, ""},
		{"optional: any offered set is fine", optional, []string{"b", "a"}, ""},
		{"optional: unknown squad is refused", optional, []string{"q"}, commerce.SquadSelectionUnknown},
		{"automatic: empty is fine", automatic, nil, ""},
		{"automatic: any choice is refused", automatic, []string{"a"}, commerce.SquadSelectionRefused},
		{"unset policy behaves as automatic", unset, []string{"a"}, commerce.SquadSelectionRefused},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidateSquadEdit(testCase.offer, testCase.selected)
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !errors.Is(err, commerce.ErrSquadSelection) {
				t.Fatalf("err = %v, want a squad selection refusal", err)
			}
			if got := squadReason(err); got != testCase.wantErr {
				t.Fatalf("reason = %q, want %q", got, testCase.wantErr)
			}
		})
	}
}

func TestIncompleteQuoteCarriesOnlyTheCurrency(t *testing.T) {
	t.Parallel()
	quote := incompleteQuote(Session{Currency: "RUB", PromoCode: "SPRING"})
	if quote.Subtotal.Currency != "RUB" || quote.Subtotal.Amount != 0 || quote.ExternalMinor != 0 {
		t.Fatalf("incomplete quote = %+v", quote)
	}
	if quote.PromoCode != "SPRING" {
		t.Fatal("the promo code the customer entered was dropped from the incomplete quote")
	}
}
