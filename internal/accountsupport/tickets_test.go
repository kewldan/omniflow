package accountsupport

import (
	"testing"
	"time"
)

// Unread is computed from the message record, not from anything this surface
// remembers. That is what lets Telegram and the browser agree: the bot stamps
// the same column, so a reply read in a chat arrives here already read.
func TestMessageIsUnread(t *testing.T) {
	read := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		author string
		readAt time.Time
		want   bool
	}{
		{"an unread operator reply", AuthorOperator, time.Time{}, true},
		{"an operator reply already read in Telegram", AuthorOperator, read, false},
		{"an unread system message", AuthorSystem, time.Time{}, true},
		{"the customer's own message", AuthorCustomer, time.Time{}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := MessageIsUnread(testCase.author, testCase.readAt); got != testCase.want {
				t.Fatalf("MessageIsUnread(%q, %v) = %v", testCase.author, testCase.readAt, got)
			}
		})
	}
}

// The two predicates deliberately disagree about a resolved ticket: it no longer
// occupies an open slot, but a customer may still answer it — and answering
// reopens it. Anything deriving one from the other would lose that.
func TestTicketStatusPredicates(t *testing.T) {
	cases := []struct {
		status    string
		open      bool
		canReply  bool
		statusSet string
	}{
		{StatusOpen, true, true, "open"},
		{StatusPending, true, true, "pending"},
		{StatusResolved, false, true, "resolved"},
		{StatusClosed, false, false, "closed"},
		{StatusMerged, false, false, "merged"},
	}
	for _, testCase := range cases {
		t.Run(testCase.statusSet, func(t *testing.T) {
			if got := TicketIsOpen(testCase.status); got != testCase.open {
				t.Fatalf("TicketIsOpen(%q) = %v", testCase.status, got)
			}
			if got := TicketAcceptsReply(testCase.status); got != testCase.canReply {
				t.Fatalf("TicketAcceptsReply(%q) = %v", testCase.status, got)
			}
		})
	}
}

// A cursor has to survive the round trip exactly, because it is compared against
// a timestamp column: a lost fraction of a second repeats or skips a row.
func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 8, 12, 10, 30, 15, 123456789, time.UTC)
	const id = "2f1c0c2e-0000-4000-8000-000000000000"
	decoded := DecodeCursor(EncodeCursor(at, id))
	if !decoded.At.Equal(at) || decoded.ID != id {
		t.Fatalf("round trip produced %v %q", decoded.At, decoded.ID)
	}
	if EncodeCursor(at, "") != "" {
		t.Fatal("an empty identifier must produce no cursor")
	}
}

// An unreadable cursor asks for the first page rather than an error. A customer
// who bookmarked a stale link should see their inbox, not a failure.
func TestDecodeCursorRejectsRubbishWithoutFailing(t *testing.T) {
	for _, value := range []string{
		"", "nonsense", "not-a-time|2f1c0c2e-0000-4000-8000-000000000000",
		"2026-08-12T10:30:15Z|not-a-uuid",
		"2026-08-12T10:30:15Z|'; DROP TABLE support_tickets; --",
	} {
		if cursor := DecodeCursor(value); cursor.set() {
			t.Fatalf("DecodeCursor(%q) produced a position", value)
		}
	}
}

func TestPageSizeClamps(t *testing.T) {
	if got := pageSize(0, 20, 100); got != 20 {
		t.Fatalf("an unspecified size became %d", got)
	}
	if got := pageSize(5000, 20, 100); got != 100 {
		t.Fatalf("an oversized request became %d", got)
	}
	if got := pageSize(37, 20, 100); got != 37 {
		t.Fatalf("a reasonable request became %d", got)
	}
}

// The subject falls back to the first line of the message, the same way the bot
// names a ticket, so one conversation is not called two different things
// depending on where it was opened.
func TestFirstLineNamesATicket(t *testing.T) {
	if got := firstLine("  Cannot connect\nSince this morning  "); got != "Cannot connect" {
		t.Fatalf("firstLine produced %q", got)
	}
}
