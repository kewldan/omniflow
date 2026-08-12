package accountreferral

import "testing"

// A referral link is the one artefact a customer hands to somebody else, so the
// failure that matters is producing a plausible-looking URL that goes nowhere:
// the customer only discovers it after they have already shared it.
func TestReferralLinkIsEitherUsableOrAbsentWithAReason(t *testing.T) {
	cases := []struct {
		name      string
		publicURL string
		code      string
		link      string
		reason    string
	}{
		{
			name: "configured origin", publicURL: "https://vpn.example.com", code: "ABCDEFGHIJ",
			link: "https://vpn.example.com/account/sign-in?ref=ABCDEFGHIJ",
		},
		{
			name: "trailing slash is not doubled", publicURL: "https://vpn.example.com/", code: "ABCDEFGHIJ",
			link: "https://vpn.example.com/account/sign-in?ref=ABCDEFGHIJ",
		},
		{
			name: "installation behind a path prefix", publicURL: "https://example.com/omniflow", code: "ABCDEFGHIJ",
			link: "https://example.com/omniflow/account/sign-in?ref=ABCDEFGHIJ",
		},
		{
			name: "no public url", publicURL: "", code: "ABCDEFGHIJ",
			reason: "public_url_not_configured",
		},
		{
			name: "whitespace is not a public url", publicURL: "   ", code: "ABCDEFGHIJ",
			reason: "public_url_not_configured",
		},
		{
			name: "a bare host is not an origin", publicURL: "vpn.example.com", code: "ABCDEFGHIJ",
			reason: "public_url_not_configured",
		},
		{
			name: "no code yet", publicURL: "https://vpn.example.com", code: "",
			reason: "no_code",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			link, reason := ReferralLink(testCase.publicURL, testCase.code)
			if link != testCase.link {
				t.Fatalf("link = %q, want %q", link, testCase.link)
			}
			if reason != testCase.reason {
				t.Fatalf("reason = %q, want %q", reason, testCase.reason)
			}
			if link == "" && reason == "" {
				t.Fatal("an absent link must always carry a reason")
			}
		})
	}
}

// Reward state answers "is this money mine?". A reversal has to win over every
// other signal, because the balance already reflects it and any other answer is
// contradicted by the number next to it.
func TestRewardStateReportsAReversalOverEverythingElse(t *testing.T) {
	cases := []struct {
		reversed bool
		review   string
		want     string
	}{
		{reversed: false, review: "clear", want: RewardQualified},
		{reversed: false, review: "held", want: RewardPending},
		{reversed: false, review: "rejected", want: RewardRejected},
		{reversed: true, review: "clear", want: RewardRejected},
		{reversed: true, review: "held", want: RewardRejected},
		{reversed: true, review: "rejected", want: RewardRejected},
	}
	for _, testCase := range cases {
		if got := RewardState(testCase.reversed, testCase.review); got != testCase.want {
			t.Fatalf("RewardState(%v, %q) = %q, want %q",
				testCase.reversed, testCase.review, got, testCase.want)
		}
	}
}

// A cursor is handed to the client and handed back. An unreadable one has to
// mean "start at the beginning" rather than fail the request, and a well-formed
// one has to survive the round trip intact.
func TestCursorRoundTripsAndToleratesRubbish(t *testing.T) {
	const id = "2f1c0c2e-0000-4000-8000-000000000000"
	encoded := encodeCursor(mustTime(t, "2026-08-12T10:04:05.123456789Z"), id)
	decoded := decodeCursor(encoded)
	if decoded.ID != id {
		t.Fatalf("identifier did not survive: %q", decoded.ID)
	}
	if !decoded.At.Equal(mustTime(t, "2026-08-12T10:04:05.123456789Z")) {
		t.Fatalf("instant did not survive: %s", decoded.At)
	}

	for _, rubbish := range []string{"", "banana", "not-a-time|" + id, "2026-08-12T10:04:05Z|not-a-uuid"} {
		if position := decodeCursor(rubbish); position.ID != "" {
			t.Fatalf("decodeCursor(%q) produced a position; want the first page", rubbish)
		}
	}
	if encodeCursor(mustTime(t, "2026-08-12T10:04:05Z"), "") != "" {
		t.Fatal("a page with no last row must not produce a cursor")
	}
}
