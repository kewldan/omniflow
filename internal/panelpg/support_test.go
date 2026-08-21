package panelpg

import "testing"

// TestDeliveryStateNamesWhatTheDeskNeedsToKnow pins the word the desk shows
// for each combination of message stamp and delivery row. The case that
// matters most is a customer who blocked the bot: that message used to read
// "queued" forever, and now reads "undeliverable".
func TestDeliveryStateNamesWhatTheDeskNeedsToKnow(t *testing.T) {
	cases := []struct {
		name      string
		sender    string
		delivered bool
		status    string
		failures  int32
		want      string
	}{
		{"customer message is never pushed", "customer", false, "", 0, ""},
		{"nothing recorded yet", "operator", false, "", 0, "queued"},
		{"accepted by telegram", "operator", true, "sent", 0, "delivered"},
		{"stamp wins over a stale row", "operator", true, "failed", 2, "delivered"},
		{"blocked bot", "operator", false, "suppressed", 0, "undeliverable"},
		{"no telegram identity", "system", false, "suppressed", 0, "undeliverable"},
		{"one transport failure", "operator", false, "failed", 1, "retrying"},
		{"out of retries", "operator", false, "failed", 3, "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deliveryState(tc.sender, tc.delivered, tc.status, tc.failures); got != tc.want {
				t.Fatalf("deliveryState = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSupportSystemNoticeSpeaksTheCustomersLanguage checks that every event
// the desk announces has wording in both languages, that the language follows
// the customer rather than the operator, and that an unknown event produces
// nothing rather than an empty message.
func TestSupportSystemNoticeSpeaksTheCustomersLanguage(t *testing.T) {
	for _, event := range []string{"resolved", "closed", "merged"} {
		english := supportSystemNotice("en", event, "Billing")
		russian := supportSystemNotice("RU", event, "Оплата")
		if english == "" || russian == "" || english == russian {
			t.Fatalf("event %s: en=%q ru=%q", event, english, russian)
		}
	}
	if got := supportSystemNotice("en", "merged", "Billing"); got != "Your request “Billing” was merged into this conversation; it continues here." {
		t.Fatalf("merged notice names the absorbed subject: %q", got)
	}
	if got := supportSystemNotice("en", "merged", "   "); got == "" || got == supportSystemNotice("en", "merged", "Billing") {
		t.Fatalf("a merged notice without a subject still reads sensibly: %q", got)
	}
	if got := supportSystemNotice("en", "assigned", ""); got != "" {
		t.Fatalf("an event the customer is not told about produced %q", got)
	}
}
