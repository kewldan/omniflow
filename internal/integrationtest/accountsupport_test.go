//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/accountpg"
	"github.com/omniflow/omniflow/internal/accountsupport"
	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// newAccountSupport builds the customer web surface against the migrated
// database, with attachments written to a directory the test owns.
func newAccountSupport(t *testing.T, harness *harness) *accountsupport.Service {
	t.Helper()
	service, err := accountsupport.New(harness.pool, accountsupport.Options{
		AttachmentDirectory: t.TempDir(),
		Logger:              slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("build customer support surface: %v", err)
	}
	return service
}

// newCustomerTicket opens a conversation the way the panel does, so the tests
// exercise the create path rather than a hand-written insert.
func newCustomerTicket(
	ctx context.Context, t *testing.T, support *accountsupport.Service, customerID, subject string,
) string {
	t.Helper()
	conversation, err := support.CreateTicket(ctx, accountsupport.NewTicket{
		CustomerID: customerID, Subject: subject, Body: "I need help with " + subject,
	})
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	return conversation.Ticket.ID
}

// TestAnInternalNoteNeverReachesTheCustomerPanel is the disclosure gate. The
// guarantee is structural rather than a filter: no customer-facing query in
// accountsupport names support_notes, so there is no code path where a note
// could be selected and then have to be removed again.
func TestAnInternalNoteNeverReachesTheCustomerPanel(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	support := newAccountSupport(t, harness)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "desk-web@example.test")
	customerID := harness.customer(ctx, t)
	ticketID := newCustomerTicket(ctx, t, support, customerID, "Refund")

	const note = "Chargeback risk — do not promise a refund"
	if _, err := operations.AddNote(ctx, ticketID, note, actor); err != nil {
		t.Fatalf("add note: %v", err)
	}
	if _, err := operations.Reply(ctx, panelpg.ReplyInput{
		TicketID: ticketID, Body: "We are looking into it", DedupeKey: "web-note-1",
	}, actor); err != nil {
		t.Fatalf("operator reply: %v", err)
	}

	conversation, err := support.Conversation(ctx, customerID, ticketID)
	if err != nil {
		t.Fatalf("read conversation: %v", err)
	}
	sawReply := false
	for _, message := range conversation.Messages {
		if strings.Contains(message.Body, "Chargeback") {
			t.Fatal("an internal note reached the customer conversation")
		}
		if message.Author == accountsupport.AuthorOperator {
			sawReply = true
		}
	}
	if !sawReply {
		t.Fatal("the operator reply is missing from the customer conversation")
	}
}

// TestReadStateIsSharedBetweenTheWebAndTelegram is the synchronisation the brief
// asks for in both directions. The state lives on the message record, so neither
// surface keeps its own idea of what has been seen.
func TestReadStateIsSharedBetweenTheWebAndTelegram(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	support := newAccountSupport(t, harness)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "sync@example.test")

	store, err := botapp.NewPostgresStore(ctx, harness.url)
	if err != nil {
		t.Fatalf("build bot store: %v", err)
	}

	customerID := harness.customer(ctx, t)
	ticketID := newCustomerTicket(ctx, t, support, customerID, "Connection")
	if _, err = operations.Reply(ctx, panelpg.ReplyInput{
		TicketID: ticketID, Body: "Try reinstalling the profile", DedupeKey: "sync-1",
	}, actor); err != nil {
		t.Fatalf("operator reply: %v", err)
	}

	page, err := support.Tickets(ctx, customerID, "", 10)
	if err != nil {
		t.Fatalf("list tickets: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Unread != 1 {
		t.Fatalf("expected one ticket with one unread reply, got %+v", page.Items)
	}

	// Read it in Telegram. The browser must not still show it as unread.
	if err = store.MarkTicketRead(ctx, customerID, ticketID); err != nil {
		t.Fatalf("mark read in the bot: %v", err)
	}
	conversation, err := support.Conversation(ctx, customerID, ticketID)
	if err != nil {
		t.Fatalf("read conversation: %v", err)
	}
	if conversation.Ticket.Unread != 0 {
		t.Fatalf("the web still reports %d unread after Telegram read it", conversation.Ticket.Unread)
	}
	for _, message := range conversation.Messages {
		if message.Unread {
			t.Fatal("a message read in Telegram is still unread on the web")
		}
	}

	// And the other way round: a second reply read in the browser must not stay
	// bold in the chat.
	if _, err = operations.Reply(ctx, panelpg.ReplyInput{
		TicketID: ticketID, Body: "Any change?", DedupeKey: "sync-2",
	}, actor); err != nil {
		t.Fatalf("second operator reply: %v", err)
	}
	if _, err = support.MarkRead(ctx, customerID, ticketID); err != nil {
		t.Fatalf("mark read on the web: %v", err)
	}
	tickets, err := store.Tickets(ctx, customerID, 10)
	if err != nil {
		t.Fatalf("list tickets in the bot: %v", err)
	}
	if len(tickets) != 1 || tickets[0].UnreadCount != 0 {
		t.Fatalf("the bot still reports unread after the web read it: %+v", tickets)
	}
	var stillUnread int
	if err = harness.pool.QueryRow(ctx,
		`SELECT count(*)::integer FROM support_messages
		 WHERE ticket_id = $1::uuid AND sender <> 'customer' AND read_at IS NULL`,
		ticketID).Scan(&stillUnread); err != nil {
		t.Fatalf("count unstamped messages: %v", err)
	}
	if stillUnread != 0 {
		t.Fatalf("%d messages were left without a read stamp", stillUnread)
	}
}

// TestAnAttachmentIsRefusedUnlessItPassesEveryRule covers the two ways an upload
// is turned away and the one way it is accepted. An unknown type is refused
// rather than guessed at, which is the difference between an allowlist and a
// suggestion.
func TestAnAttachmentIsRefusedUnlessItPassesEveryRule(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	support := newAccountSupport(t, harness)
	customerID := harness.customer(ctx, t)
	ticketID := newCustomerTicket(ctx, t, support, customerID, "Screenshot")

	limits := support.Limits()
	oversized := make([]byte, limits.MaxAttachmentBytes+1)
	_, err := support.Attach(ctx, accountsupport.NewAttachment{
		CustomerID: customerID, TicketID: ticketID,
		FileName: "huge.png", MediaType: "image/png", Content: oversized,
	})
	if !errors.Is(err, accountsupport.ErrAttachmentTooLarge) {
		t.Fatalf("an oversized upload reported %v", err)
	}

	_, err = support.Attach(ctx, accountsupport.NewAttachment{
		CustomerID: customerID, TicketID: ticketID,
		FileName: "payload.html", MediaType: "text/html", Content: []byte("<script>"),
	})
	if !errors.Is(err, accountsupport.ErrAttachmentMediaType) {
		t.Fatalf("a disallowed media type reported %v", err)
	}

	accepted, err := support.Attach(ctx, accountsupport.NewAttachment{
		CustomerID: customerID, TicketID: ticketID,
		FileName: "../../screenshot.png", MediaType: "image/png", Content: []byte("PNG bytes"),
	})
	if err != nil {
		t.Fatalf("a permitted upload was refused: %v", err)
	}
	if accepted.FileName != "screenshot.png" {
		t.Fatalf("the stored name is %q; the path components survived", accepted.FileName)
	}
	if accepted.Kind != "photo" || !accepted.Downloadable {
		t.Fatalf("stored attachment reads %+v", accepted)
	}

	// The bytes come back to the owner, and to nobody else.
	_, content, err := support.Attachment(ctx, customerID, accepted.ID)
	if err != nil {
		t.Fatalf("download own attachment: %v", err)
	}
	if string(content) != "PNG bytes" {
		t.Fatalf("the download returned %q", content)
	}
	stranger := harness.customer(ctx, t)
	if _, _, err = support.Attachment(ctx, stranger, accepted.ID); !errors.Is(err, accountsupport.ErrNotFound) {
		t.Fatalf("a stranger's download reported %v, want not found", err)
	}
}

// TestConsentIsAppendedNeverEdited is the record-keeping rule. A consent history
// that can be rewritten is not evidence that consent was ever given.
func TestConsentIsAppendedNeverEdited(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	support := newAccountSupport(t, harness)
	customerID := harness.customer(ctx, t)

	granted, revoked := true, false
	if _, err := support.UpdatePreferences(ctx, customerID,
		accountsupport.PreferencesUpdate{Marketing: &granted},
		accountsupport.RequestContext{RequestID: "request-grant"}); err != nil {
		t.Fatalf("grant marketing consent: %v", err)
	}
	if _, err := support.UpdatePreferences(ctx, customerID,
		accountsupport.PreferencesUpdate{Marketing: &revoked},
		accountsupport.RequestContext{RequestID: "request-revoke"}); err != nil {
		t.Fatalf("revoke marketing consent: %v", err)
	}

	rows, err := harness.pool.Query(ctx,
		`SELECT granted, source, COALESCE(request_id, '') FROM consent_records
		 WHERE user_id = $1::uuid AND purpose = 'marketing' ORDER BY occurred_at, id`, customerID)
	if err != nil {
		t.Fatalf("read consent history: %v", err)
	}
	defer rows.Close()
	type record struct {
		granted           bool
		source, requestID string
	}
	history := make([]record, 0, 2)
	for rows.Next() {
		var entry record
		if err = rows.Scan(&entry.granted, &entry.source, &entry.requestID); err != nil {
			t.Fatalf("scan consent record: %v", err)
		}
		history = append(history, entry)
	}
	if len(history) != 2 {
		t.Fatalf("expected two consent records, got %d", len(history))
	}
	if !history[0].granted || history[1].granted {
		t.Fatalf("the history does not read grant-then-revoke: %+v", history)
	}
	for _, entry := range history {
		if entry.source != "customer_web" {
			t.Fatalf("a consent record names the surface %q", entry.source)
		}
		if entry.requestID == "" {
			t.Fatal("a consent record carries no request identifier")
		}
	}

	// The unsubscribe writes the suppression the pipeline reads, under the one
	// reason a customer action is allowed to claim.
	if _, err = support.Unsubscribe(ctx, customerID,
		accountsupport.RequestContext{RequestID: "request-unsubscribe"}); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	preferences, err := support.Preferences(ctx, customerID)
	if err != nil {
		t.Fatalf("read preferences: %v", err)
	}
	if preferences.Suppression == nil || preferences.Suppression.Reason != "customer_request" {
		t.Fatalf("suppression reads %+v", preferences.Suppression)
	}
	if preferences.Marketing.Enabled {
		t.Fatal("marketing consent survived an unsubscribe")
	}
	// Transactional notifications are untouched: an unsubscribe must not be a way
	// to stop hearing that a subscription is about to end.
	if !preferences.Notifications.Expiry || !preferences.Notifications.Renewal {
		t.Fatalf("an unsubscribe silenced transactional notifications: %+v", preferences.Notifications)
	}
	var count int
	if err = harness.pool.QueryRow(ctx,
		`SELECT count(*)::integer FROM consent_records WHERE user_id = $1::uuid AND purpose = 'marketing'`,
		customerID).Scan(&count); err != nil {
		t.Fatalf("count consent records: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected three appended consent records, got %d", count)
	}
}

// TestATicketBelongsToExactlyOneCustomer is the isolation rule. "Not yours" and
// "does not exist" have to be the same answer, or an identifier becomes a way to
// discover that somebody else's ticket exists.
func TestATicketBelongsToExactlyOneCustomer(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	support := newAccountSupport(t, harness)
	owner := harness.customer(ctx, t)
	stranger := harness.customer(ctx, t)
	ticketID := newCustomerTicket(ctx, t, support, owner, "Private")

	if _, err := support.Conversation(ctx, stranger, ticketID); !errors.Is(err, accountsupport.ErrNotFound) {
		t.Fatalf("a stranger read the conversation with %v", err)
	}
	if _, err := support.Reply(ctx, accountsupport.NewMessage{
		CustomerID: stranger, TicketID: ticketID, Body: "Let me in",
	}); !errors.Is(err, accountsupport.ErrNotFound) {
		t.Fatalf("a stranger replied with %v", err)
	}
	if _, err := support.MarkRead(ctx, stranger, ticketID); !errors.Is(err, accountsupport.ErrNotFound) {
		t.Fatalf("a stranger marked it read with %v", err)
	}
	if _, err := support.Close(ctx, stranger, ticketID); !errors.Is(err, accountsupport.ErrNotFound) {
		t.Fatalf("a stranger closed it with %v", err)
	}
	page, err := support.Tickets(ctx, stranger, "", 10)
	if err != nil {
		t.Fatalf("list a stranger's tickets: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("a stranger's inbox contains %d tickets", len(page.Items))
	}
}

// TestTheOpenTicketQuotaBoundsTheOperatorQueue proves the flood bound, and that
// closing one frees a slot rather than the quota being permanent.
func TestTheOpenTicketQuotaBoundsTheOperatorQueue(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	support := newAccountSupport(t, harness)
	customerID := harness.customer(ctx, t)

	limits := support.Limits()
	first := ""
	for index := 0; index < limits.MaxOpenTickets; index++ {
		conversation, err := support.CreateTicket(ctx, accountsupport.NewTicket{
			CustomerID: customerID, Subject: "Question", Body: "Please help",
		})
		if err != nil {
			t.Fatalf("create ticket %d: %v", index, err)
		}
		if first == "" {
			first = conversation.Ticket.ID
		}
	}
	_, err := support.CreateTicket(ctx, accountsupport.NewTicket{
		CustomerID: customerID, Subject: "One too many", Body: "Please help",
	})
	if !errors.Is(err, accountsupport.ErrTooManyOpenTickets) {
		t.Fatalf("the quota reported %v", err)
	}

	if _, err = support.Close(ctx, customerID, first); err != nil {
		t.Fatalf("close a ticket: %v", err)
	}
	if _, err = support.CreateTicket(ctx, accountsupport.NewTicket{
		CustomerID: customerID, Subject: "Now allowed", Body: "Please help",
	}); err != nil {
		t.Fatalf("a freed slot was not usable: %v", err)
	}

	// A closed conversation refuses a reply until it is reopened, and reopening
	// is the explicit action that allows one.
	if _, err = support.Reply(ctx, accountsupport.NewMessage{
		CustomerID: customerID, TicketID: first, Body: "Actually, one more thing",
	}); !errors.Is(err, accountsupport.ErrTicketClosed) {
		t.Fatalf("a closed conversation accepted a reply with %v", err)
	}
	if _, err = support.Reopen(ctx, customerID, first); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err = support.Reply(ctx, accountsupport.NewMessage{
		CustomerID: customerID, TicketID: first, Body: "Actually, one more thing",
	}); err != nil {
		t.Fatalf("a reopened conversation refused a reply: %v", err)
	}
}

// TestAResubmittedFormDoesNotOpenASecondTicket covers the idempotency key on the
// create route. A duplicate ticket is worse than a duplicate row: it is a second
// place an operator has to answer the same question.
func TestAResubmittedFormDoesNotOpenASecondTicket(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	support := newAccountSupport(t, harness)
	customerID := harness.customer(ctx, t)

	input := accountsupport.NewTicket{
		CustomerID: customerID, Subject: "Billing", Body: "I was charged twice",
		IdempotencyKey: "form-submission-1",
	}
	first, err := support.CreateTicket(ctx, input)
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	second, err := support.CreateTicket(ctx, input)
	if err != nil {
		t.Fatalf("replayed create: %v", err)
	}
	if first.Ticket.ID != second.Ticket.ID {
		t.Fatalf("a replayed submission opened a second ticket: %s and %s",
			first.Ticket.ID, second.Ticket.ID)
	}
	if len(second.Messages) != 1 {
		t.Fatalf("expected one message after a replay, got %d", len(second.Messages))
	}
}

// TestOnlyPublishedNewsIsReadable covers the visibility predicate and the shared
// read state. Unpublishing keeps `published_at` set on purpose, so a timestamp
// window alone would still show something an operator has taken down.
func TestOnlyPublishedNewsIsReadable(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	support := newAccountSupport(t, harness)
	customerID := harness.customer(ctx, t)

	published := harness.newsPost(ctx, t, "released-notes", "published")
	taken := harness.newsPost(ctx, t, "withdrawn-notes", "unpublished")

	page, err := support.News(ctx, customerID, "en", "", 10)
	if err != nil {
		t.Fatalf("list news: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != published {
		t.Fatalf("the inbox contains %+v", page.Items)
	}
	if page.Unread != 1 {
		t.Fatalf("unread count reads %d", page.Unread)
	}
	if err = support.MarkNewsRead(ctx, customerID, taken); !errors.Is(err, accountsupport.ErrNotFound) {
		t.Fatalf("an unpublished post reported %v", err)
	}
	if err = support.MarkNewsRead(ctx, customerID, published); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	// Reading twice is not an error, and the row the bot reads is the row this
	// wrote, so the post is read on both surfaces at once.
	if err = support.MarkNewsRead(ctx, customerID, published); err != nil {
		t.Fatalf("second mark read: %v", err)
	}
	after, err := support.News(ctx, customerID, "en", "", 10)
	if err != nil {
		t.Fatalf("list news again: %v", err)
	}
	if after.Unread != 0 || !after.Items[0].Read {
		t.Fatalf("the post is still unread: %+v", after)
	}
}

// newsPost inserts one localised post in a given lifecycle state.
func (harness *harness) newsPost(
	ctx context.Context, t *testing.T, slug, status string,
) string {
	t.Helper()
	var id string
	if err := harness.pool.QueryRow(ctx,
		`INSERT INTO news_posts (slug, category, class, status, published_at)
		 VALUES ($1, 'announcement', 'transactional', $2, now() - interval '1 hour')
		 RETURNING id::text`, slug, status).Scan(&id); err != nil {
		t.Fatalf("create news post: %v", err)
	}
	if _, err := harness.pool.Exec(ctx,
		`INSERT INTO news_post_localizations (post_id, locale, title, body)
		 VALUES ($1::uuid, 'en', $2, 'Body text')`, id, slug); err != nil {
		t.Fatalf("localise news post: %v", err)
	}
	return id
}

// TestAnInvalidMessageIsRejectedBeforeItReachesTheQueue keeps the shared error
// mapping honest: the domain's own validation error is what the transport turns
// into a 422, so the reason travels with it.
func TestAnInvalidMessageIsRejectedBeforeItReachesTheQueue(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	support := newAccountSupport(t, harness)
	customerID := harness.customer(ctx, t)

	_, err := support.CreateTicket(ctx, accountsupport.NewTicket{
		CustomerID: customerID, Subject: "Empty", Body: "   ",
	})
	if !errors.Is(err, accountpg.ErrInvalidInput) {
		t.Fatalf("an empty message reported %v", err)
	}
	_, err = support.CreateTicket(ctx, accountsupport.NewTicket{
		CustomerID: customerID, Subject: "Long",
		Body: strings.Repeat("x", accountsupport.MaxMessageLength+1),
	})
	if !errors.Is(err, accountpg.ErrInvalidInput) {
		t.Fatalf("an overlong message reported %v", err)
	}
}

// TestTheOpenQuotaCannotBeSteppedAroundByReopening closes the two side doors
// the create-time quota left open: an explicit reopen, and a reply on a
// resolved ticket that reopens it. Both now take a slot the customer must have.
func TestTheOpenQuotaCannotBeSteppedAroundByReopening(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "quota@example.test")
	support, err := accountsupport.New(harness.pool, accountsupport.Options{
		AttachmentDirectory: t.TempDir(),
		Logger:              slog.New(slog.DiscardHandler),
		Limits: accountsupport.Limits{
			MaxAttachmentBytes: 1 << 20, AllowedMediaTypes: []string{"text/plain"},
			MaxOpenTickets: 2, MaxAttachmentsPerTicket: 1,
		},
	})
	if err != nil {
		t.Fatalf("build support surface: %v", err)
	}
	customerID := harness.customer(ctx, t)

	first := newCustomerTicket(ctx, t, support, customerID, "First")
	newCustomerTicket(ctx, t, support, customerID, "Second")
	if _, err = operations.SetTicketStatus(ctx, first, "resolved", actor); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// A resolved ticket frees its slot, so a third conversation opens.
	third := newCustomerTicket(ctx, t, support, customerID, "Third")

	// Both slots are taken again. Neither way of waking the resolved ticket
	// may take a third.
	if _, err = support.Reply(ctx, accountsupport.NewMessage{
		CustomerID: customerID, TicketID: first, Body: "It is back",
	}); !errors.Is(err, accountsupport.ErrTooManyOpenTickets) {
		t.Fatalf("a reply that would reopen past the quota returned %v", err)
	}
	if _, err = support.Reopen(ctx, customerID, first); !errors.Is(err, accountsupport.ErrTooManyOpenTickets) {
		t.Fatalf("a reopen past the quota returned %v", err)
	}
	if _, err = support.Attach(ctx, accountsupport.NewAttachment{
		CustomerID: customerID, TicketID: first, FileName: "log.txt",
		MediaType: "text/plain", Content: []byte("still broken"),
	}); !errors.Is(err, accountsupport.ErrTooManyOpenTickets) {
		t.Fatalf("an upload that would reopen past the quota returned %v", err)
	}

	// Closing one makes room, and the reply then reopens as it always did.
	if _, err = support.Close(ctx, customerID, third); err != nil {
		t.Fatalf("close: %v", err)
	}
	conversation, err := support.Reply(ctx, accountsupport.NewMessage{
		CustomerID: customerID, TicketID: first, Body: "It is back",
	})
	if err != nil {
		t.Fatalf("reply once a slot is free: %v", err)
	}
	if conversation.Ticket.Status != accountsupport.StatusOpen {
		t.Fatalf("the reply left the ticket %s, want open", conversation.Ticket.Status)
	}

	// Files per conversation are bounded too, and a replayed upload with the
	// same key is the same file rather than a second one against the bound.
	attached, err := support.Attach(ctx, accountsupport.NewAttachment{
		CustomerID: customerID, TicketID: first, FileName: "log.txt",
		MediaType: "text/plain", Content: []byte("the log"), IdempotencyKey: "upload-1",
	})
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}
	replayed, err := support.Attach(ctx, accountsupport.NewAttachment{
		CustomerID: customerID, TicketID: first, FileName: "log.txt",
		MediaType: "text/plain", Content: []byte("the log"), IdempotencyKey: "upload-1",
	})
	if err != nil {
		t.Fatalf("replayed upload: %v", err)
	}
	if replayed.ID != attached.ID || replayed.MessageID != attached.MessageID {
		t.Fatalf("a replayed upload produced a second attachment: %+v vs %+v", replayed, attached)
	}
	if _, err = support.Attach(ctx, accountsupport.NewAttachment{
		CustomerID: customerID, TicketID: first, FileName: "more.txt",
		MediaType: "text/plain", Content: []byte("another"), IdempotencyKey: "upload-2",
	}); !errors.Is(err, accountsupport.ErrTooManyAttachments) {
		t.Fatalf("a second distinct file past the bound returned %v", err)
	}
}

// TestACustomerCannotLiftAnOperatorsSuppression is the two-click defect: an
// unsubscribe used to overwrite a bounce or complaint hold with
// customer_request, and re-enabling marketing — which lifts only
// customer_request rows — then cleared the operator's finding.
func TestACustomerCannotLiftAnOperatorsSuppression(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	support := newAccountSupport(t, harness)
	customerID := harness.customer(ctx, t)

	if _, err := harness.pool.Exec(ctx, `INSERT INTO communication_suppressions (user_id, reason, note)
		VALUES ($1::uuid, 'bounced', 'three hard bounces')`, customerID); err != nil {
		t.Fatalf("place an operator suppression: %v", err)
	}

	if _, err := support.Unsubscribe(ctx, customerID,
		accountsupport.RequestContext{RequestID: "request-unsubscribe"}); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	var reason string
	if err := harness.pool.QueryRow(ctx,
		`SELECT reason FROM communication_suppressions WHERE user_id = $1::uuid`, customerID).Scan(&reason); err != nil {
		t.Fatalf("read suppression: %v", err)
	}
	if reason != "bounced" {
		t.Fatalf("an unsubscribe rewrote the operator's suppression to %q", reason)
	}

	granted := true
	if _, err := support.UpdatePreferences(ctx, customerID,
		accountsupport.PreferencesUpdate{Marketing: &granted},
		accountsupport.RequestContext{RequestID: "request-grant"}); err != nil {
		t.Fatalf("re-enable marketing: %v", err)
	}
	if err := harness.pool.QueryRow(ctx,
		`SELECT reason FROM communication_suppressions WHERE user_id = $1::uuid`, customerID).Scan(&reason); err != nil {
		t.Fatalf("the operator's suppression was lifted by the customer: %v", err)
	}
	if reason != "bounced" {
		t.Fatalf("the suppression now reads %q", reason)
	}

	// A customer with no hold still gets one from their own unsubscribe, and
	// still lifts it by opting back in — the original behaviour, untouched.
	other := harness.customer(ctx, t)
	if _, err := support.Unsubscribe(ctx, other,
		accountsupport.RequestContext{RequestID: "request-unsubscribe-2"}); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if _, err := support.UpdatePreferences(ctx, other,
		accountsupport.PreferencesUpdate{Marketing: &granted},
		accountsupport.RequestContext{RequestID: "request-grant-2"}); err != nil {
		t.Fatalf("re-enable marketing: %v", err)
	}
	var remaining int
	if err := harness.pool.QueryRow(ctx,
		`SELECT count(*)::integer FROM communication_suppressions WHERE user_id = $1::uuid`, other).Scan(&remaining); err != nil {
		t.Fatalf("count suppressions: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("a customer_request suppression survived the customer opting back in")
	}
}
