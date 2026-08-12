package accountshop

import (
	"errors"
	"testing"
)

// A purchase must present the handle the review step returned. Anything else —
// including input that would normalise to the same username — is sent back
// through the review, because the point of the step is that the customer saw
// the exact string the gateway will be given.
func TestOnlyAReviewedHandleMayBePurchasedAgainst(t *testing.T) {
	cases := []struct {
		name      string
		submitted string
		want      string
		wantErr   error
	}{
		{name: "the reviewed form", submitted: "recipient_one", want: "recipient_one"},
		{
			name: "surrounding whitespace is not a different handle",
			// Trimming here matches what the review returned; the customer saw
			// the same characters.
			submitted: "  recipient_one  ", want: "recipient_one",
		},
		{
			name:      "a handle still carrying its @ never went through the review",
			submitted: "@recipient_one", wantErr: ErrRecipientNotReviewed,
		},
		{
			name:      "a pasted profile link never went through the review",
			submitted: "https://t.me/recipient_one", wantErr: ErrRecipientNotReviewed,
		},
		{
			name:      "nothing usable is refused as a username rather than as a review failure",
			submitted: "no", wantErr: ErrRecipientInvalid,
		},
		{name: "empty", submitted: "", wantErr: ErrRecipientInvalid},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := reviewedRecipient(testCase.submitted)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("error = %v, want %v", err, testCase.wantErr)
			}
			if got != testCase.want {
				t.Fatalf("recipient = %q, want %q", got, testCase.want)
			}
		})
	}
}
