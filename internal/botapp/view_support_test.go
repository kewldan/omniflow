package botapp

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestSupportTicketActionsMirrorTheWebPanel pins the buttons under a
// conversation to the state machine the web panel enforces: reply while the
// ticket can take a message, reopen only when there is something to reopen,
// nothing at all once it was merged away.
func TestSupportTicketActionsMirrorTheWebPanel(t *testing.T) {
	cases := []struct {
		status string
		want   []string
	}{
		{"open", []string{"ticket-reply", "ticket-close"}},
		{"pending", []string{"ticket-reply", "ticket-close"}},
		{"resolved", []string{"ticket-reply", "ticket-open", "ticket-close"}},
		{"closed", []string{"ticket-open"}},
		{"merged", nil},
		{"something-new", nil},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			actions := supportTicketActions(tc.status)
			got := make([]string, 0, len(actions))
			for _, action := range actions {
				got = append(got, action.action)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("actions for %s = %v, want %v", tc.status, got, tc.want)
			}
			// Every offered action matches what the store will accept, so a
			// button can never lead to a refusal on a fresh screen.
			for _, action := range actions {
				switch action.action {
				case "ticket-reply":
					if !ticketAcceptsReply(tc.status) {
						t.Fatalf("%s offers Reply but the store refuses it", tc.status)
					}
				case "ticket-open":
					if !ticketCanReopen(tc.status) {
						t.Fatalf("%s offers Reopen but the store ignores it", tc.status)
					}
				}
			}
		})
	}
}

// TestEveryTicketStatusHasWords is the raw-key defect: pending, resolved, and
// merged used to render as `support.status.pending` in both languages.
func TestEveryTicketStatusHasWords(t *testing.T) {
	for _, status := range []string{"open", "pending", "resolved", "closed", "merged"} {
		for _, locale := range []Locale{LocaleRussian, LocaleEnglish} {
			key := "support.status." + status
			if got := text(locale, key); got == key || strings.TrimSpace(got) == "" {
				t.Fatalf("status %s has no %s wording", status, locale)
			}
		}
	}
	view := supportTicketView(LocaleEnglish, Ticket{ID: "0000", Subject: "Hi", Status: "pending", UpdatedAt: time.Now()}, nil)
	if strings.Contains(view.Text, "support.status.") {
		t.Fatalf("the ticket screen still renders a raw status key: %s", view.Text)
	}
}

// TestSupportSubmitOutcomeEndsTheSessionOnlyForFinalRefusals separates the
// refusals that end a compose session from the failures worth trying again.
func TestSupportSubmitOutcomeEndsTheSessionOnlyForFinalRefusals(t *testing.T) {
	for _, err := range []error{ErrTicketMerged, ErrTicketClosed, ErrTicketNotFound} {
		view, final := supportSubmitOutcome(LocaleEnglish, err)
		if !final || strings.TrimSpace(view.Text) == "" || view.Keyboard == nil {
			t.Fatalf("%v should end the session with an explanation and a way back", err)
		}
	}
	for _, err := range []error{nil, ErrAttachmentTooBig, errors.New("connection refused")} {
		if _, final := supportSubmitOutcome(LocaleEnglish, err); final {
			t.Fatalf("%v should not end the session", err)
		}
	}
}
