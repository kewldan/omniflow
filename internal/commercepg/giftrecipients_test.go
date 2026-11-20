package commercepg

import (
	"errors"
	"testing"
)

func TestParseGiftRecipient(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input, username, email string
		invalid                bool
	}{
		{input: ""},
		{input: "  @Friend_One ", username: "friend_one"},
		{input: "friend_one", username: "friend_one"},
		{input: "Friend@Example.com", email: "Friend@Example.com"},
		{input: "@abc", invalid: true},
		{input: "not an address", invalid: true},
		{input: "a@b", invalid: true},
	}
	for _, testCase := range cases {
		username, email, err := ParseGiftRecipient(testCase.input)
		if testCase.invalid {
			if !errors.Is(err, ErrGiftRecipientInvalid) {
				t.Errorf("%q: err = %v, want invalid", testCase.input, err)
			}
			continue
		}
		if err != nil || username != testCase.username || email != testCase.email {
			t.Errorf("%q = %q %q %v, want %q %q", testCase.input, username, email, err, testCase.username, testCase.email)
		}
	}
}
