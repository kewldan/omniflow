package botapp

import (
	"testing"
	"time"
)

// TestFreeTextTicketContinuesTheRecentLiveConversation pins where a bare
// message goes when no compose flow is in progress: the most recently active
// request that can still take a message, and only while it was active
// recently. Everything else falls through to the menu, as it always did.
func TestFreeTextTicketContinuesTheRecentLiveConversation(t *testing.T) {
	now := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	cases := []struct {
		name    string
		tickets []Ticket
		wantID  string
		wantOK  bool
	}{
		{"no conversations", nil, "", false},
		{"an open request answered an hour ago", []Ticket{
			{ID: "open", Status: "open", LastMessageAt: ago(time.Hour), UpdatedAt: ago(time.Hour)},
		}, "open", true},
		{"a pending request is waiting for the customer", []Ticket{
			{ID: "pending", Status: "pending", LastMessageAt: ago(2 * time.Hour), UpdatedAt: ago(2 * time.Hour)},
		}, "pending", true},
		{"a resolved request reopens when answered soon after", []Ticket{
			{ID: "resolved", Status: "resolved", LastMessageAt: ago(6 * time.Hour), UpdatedAt: ago(6 * time.Hour)},
		}, "resolved", true},
		{"a closed request is never continued", []Ticket{
			{ID: "closed", Status: "closed", LastMessageAt: ago(time.Minute), UpdatedAt: ago(time.Minute)},
		}, "", false},
		{"a merged request is never continued", []Ticket{
			{ID: "merged", Status: "merged", LastMessageAt: ago(time.Minute), UpdatedAt: ago(time.Minute)},
		}, "", false},
		{"an old open request goes to the menu", []Ticket{
			{ID: "stale", Status: "open", LastMessageAt: ago(freeTextWindow + time.Minute), UpdatedAt: ago(freeTextWindow + time.Minute)},
		}, "", false},
		{"the most recently active one wins", []Ticket{
			{ID: "older", Status: "open", LastMessageAt: ago(10 * time.Hour), UpdatedAt: ago(10 * time.Hour)},
			{ID: "newer", Status: "pending", LastMessageAt: ago(time.Hour), UpdatedAt: ago(time.Hour)},
			{ID: "closed", Status: "closed", LastMessageAt: ago(time.Minute), UpdatedAt: ago(time.Minute)},
		}, "newer", true},
		{"a delivered reply counts as activity", []Ticket{
			// The operator's reply bumps updated_at when it is pushed, which is
			// exactly the moment the customer is about to type back.
			{ID: "replied", Status: "open", LastMessageAt: ago(5 * 24 * time.Hour), UpdatedAt: ago(30 * time.Minute)},
		}, "replied", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ticket, ok := freeTextTicket(now, tc.tickets)
			if ok != tc.wantOK || ticket.ID != tc.wantID {
				t.Fatalf("freeTextTicket = (%q, %v), want (%q, %v)", ticket.ID, ok, tc.wantID, tc.wantOK)
			}
		})
	}
}
