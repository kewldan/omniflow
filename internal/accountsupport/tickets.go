package accountsupport

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// The ticket statuses a customer can be shown.
//
// They are the record's own values rather than a customer-facing translation of
// them. A vocabulary that differs per surface is a vocabulary that drifts, and
// the operator desk, the bot, and the panel all have to agree on what "resolved"
// means for the read state between them to make sense.
const (
	StatusOpen     = "open"
	StatusPending  = "pending"
	StatusResolved = "resolved"
	StatusClosed   = "closed"
	StatusMerged   = "merged"
)

// The message author vocabulary, matching `support_messages.sender`.
const (
	AuthorCustomer = "customer"
	AuthorOperator = "operator"
	AuthorSystem   = "system"
)

// maxConversationMessages bounds one conversation response. A support thread
// that has run past this is a thread whose beginning nobody is scrolling to, and
// an unbounded read would let one long-running ticket decide the response size
// for everybody.
const maxConversationMessages = 200

// MaxSubjectLength and MaxMessageLength are the column checks, in characters
// rather than bytes. They are exported so the panel can count down to the same
// number the API refuses at, instead of guessing a limit and being wrong for
// every alphabet but one.
const (
	MaxSubjectLength = 120
	MaxMessageLength = 4000
)

// Ticket is one conversation as the customer sees it.
type Ticket struct {
	ID       string
	Subject  string
	Status   string
	Priority string
	// Open follows the quota rule rather than plain English: a resolved ticket is
	// still readable and still answerable, but it no longer occupies one of the
	// customer's open slots, because the operator has finished with it.
	Open bool
	// CanReply is what the panel disables its composer on. It is separate from
	// Open because replying to a resolved ticket is allowed and reopens it, which
	// is exactly what a customer means when they answer "that did not fix it".
	CanReply      bool
	Unread        int
	MessageCount  int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastMessageAt time.Time
	// MergedInto names the surviving conversation when an operator folded this
	// one into it. The customer's words moved there, so a bookmarked link has
	// somewhere to send them rather than showing an empty thread.
	MergedInto string
}

// TicketPage is one page of the inbox.
type TicketPage struct {
	Items      []Ticket
	NextCursor string
}

// Message is one turn of a conversation.
type Message struct {
	ID     int64
	Author string
	Body   string
	// Unread marks a message the customer has not read on any surface. It is
	// computed from the message record, so a reply read in Telegram arrives here
	// already read.
	Unread      bool
	CreatedAt   time.Time
	Attachments []Attachment
}

// Conversation is a ticket with the messages the customer may see.
type Conversation struct {
	Ticket   Ticket
	Messages []Message
}

// NewTicket opens a conversation.
type NewTicket struct {
	CustomerID string
	Subject    string
	Body       string
	// IdempotencyKey is the request's Idempotency-Key. A resubmitted form must
	// reach the ticket that already exists rather than opening a second one, and
	// a second ticket is worse than a duplicate row: it is a second place an
	// operator has to answer the same question.
	IdempotencyKey string
}

// NewMessage continues one.
type NewMessage struct {
	CustomerID     string
	TicketID       string
	Body           string
	IdempotencyKey string
}

// ticketColumns is the customer-visible projection of a ticket.
//
// The unread count is derived from the messages rather than read from
// `support_tickets.customer_unread_count`. That column is Telegram's delivery
// counter: it is raised when a reply is successfully pushed to a chat, so a
// customer who only ever uses the browser would see zero unread forever. Counting
// unstamped messages instead puts the state where the brief requires it — on the
// record both surfaces write — and MarkRead still zeroes the stored counter, so
// the bot's own list agrees with what the browser just showed.
//
// It reads support_tickets and support_messages only. Nothing in this package
// selects from support_notes, which is what makes an internal note impossible to
// leak here rather than merely unlikely to be.
const ticketColumns = `t.id::text, t.subject, t.status, t.priority,
	t.created_at, t.updated_at, t.last_message_at,
	(SELECT count(*) FROM support_messages m
	 WHERE m.ticket_id = t.id AND m.sender <> 'customer' AND m.read_at IS NULL),
	COALESCE(t.merged_into_ticket_id::text, ''),
	(SELECT count(*) FROM support_messages m WHERE m.ticket_id = t.id)`

func scanTicket(row pgx.Row) (Ticket, error) {
	var ticket Ticket
	var unread, messageCount int64
	err := row.Scan(&ticket.ID, &ticket.Subject, &ticket.Status, &ticket.Priority,
		&ticket.CreatedAt, &ticket.UpdatedAt, &ticket.LastMessageAt, &unread,
		&ticket.MergedInto, &messageCount)
	if err != nil {
		return Ticket{}, err
	}
	ticket.Unread = int(unread)
	ticket.MessageCount = int(messageCount)
	ticket.CreatedAt = ticket.CreatedAt.UTC()
	ticket.UpdatedAt = ticket.UpdatedAt.UTC()
	ticket.LastMessageAt = ticket.LastMessageAt.UTC()
	ticket.Open = TicketIsOpen(ticket.Status)
	ticket.CanReply = TicketAcceptsReply(ticket.Status)
	return ticket, nil
}

// TicketIsOpen reports whether a status occupies one of the customer's open
// conversation slots. It is the same predicate the quota counts with, so what
// the panel labels open and what the create route refuses on cannot diverge.
func TicketIsOpen(status string) bool {
	return status == StatusOpen || status == StatusPending
}

// TicketAcceptsReply reports whether a customer may still write into a
// conversation. A merged ticket refuses because the conversation continued
// somewhere else, and a closed one refuses because reopening is deliberate.
func TicketAcceptsReply(status string) bool {
	return status == StatusOpen || status == StatusPending || status == StatusResolved
}

// MessageIsUnread reports whether one message still counts as unread for the
// customer.
//
// The customer's own messages are never unread to them, and the read stamp is
// the message's own column — the same column the bot writes when the customer
// opens the conversation in Telegram. That shared column is the whole mechanism:
// neither surface keeps a private idea of what has been seen.
func MessageIsUnread(author string, readAt time.Time) bool {
	return author != AuthorCustomer && readAt.IsZero()
}

// Tickets lists the customer's conversations, most recent activity first.
func (service *Service) Tickets(
	ctx context.Context, customerID, cursor string, limit int,
) (TicketPage, error) {
	size := pageSize(limit, 20, 100)
	position := DecodeCursor(cursor)
	var cursorAt pgtype.Timestamptz
	var cursorID pgtype.UUID
	if position.set() {
		cursorAt = pgtype.Timestamptz{Time: position.At, Valid: true}
		if err := cursorID.Scan(position.ID); err != nil {
			cursorAt, cursorID = pgtype.Timestamptz{}, pgtype.UUID{}
		}
	}
	// One row beyond the page is read so the next cursor is only emitted when
	// there is genuinely something after it. A cursor that leads to an empty page
	// makes a list look broken.
	rows, err := service.pool.Query(ctx, `SELECT `+ticketColumns+`
		FROM support_tickets t
		WHERE t.user_id = $1::uuid
		  AND ($2::timestamptz IS NULL
		       OR (t.last_message_at, t.id) < ($2::timestamptz, $3::uuid))
		ORDER BY t.last_message_at DESC, t.id DESC
		LIMIT $4`, customerID, cursorAt, cursorID, size+1)
	if err != nil {
		return TicketPage{}, err
	}
	defer rows.Close()
	page := TicketPage{Items: make([]Ticket, 0, size)}
	for rows.Next() {
		ticket, scanErr := scanTicket(rows)
		if scanErr != nil {
			return TicketPage{}, scanErr
		}
		page.Items = append(page.Items, ticket)
	}
	if err = rows.Err(); err != nil {
		return TicketPage{}, err
	}
	if len(page.Items) > size {
		last := page.Items[size-1]
		page.Items = page.Items[:size]
		page.NextCursor = EncodeCursor(last.LastMessageAt, last.ID)
	}
	return page, nil
}

// Conversation reads one ticket with its customer-visible messages.
func (service *Service) Conversation(
	ctx context.Context, customerID, ticketID string,
) (Conversation, error) {
	if !looksLikeUUID(strings.TrimSpace(ticketID)) {
		return Conversation{}, ErrNotFound
	}
	ticket, err := scanTicket(service.pool.QueryRow(ctx, `SELECT `+ticketColumns+`
		FROM support_tickets t WHERE t.id = $2::uuid AND t.user_id = $1::uuid`,
		customerID, ticketID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, err
	}
	messages, err := service.messages(ctx, ticketID)
	if err != nil {
		return Conversation{}, err
	}
	return Conversation{Ticket: ticket, Messages: messages}, nil
}

// messages reads the newest turns of a conversation and returns them oldest
// first, which is how a conversation reads.
func (service *Service) messages(ctx context.Context, ticketID string) ([]Message, error) {
	rows, err := service.pool.Query(ctx, `SELECT m.id, m.sender, m.body, m.created_at, m.read_at
		FROM support_messages m WHERE m.ticket_id = $1::uuid
		ORDER BY m.created_at DESC, m.id DESC LIMIT $2`, ticketID, maxConversationMessages)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]Message, 0, maxConversationMessages)
	for rows.Next() {
		var message Message
		var readAt pgtype.Timestamptz
		if err = rows.Scan(
			&message.ID, &message.Author, &message.Body, &message.CreatedAt, &readAt,
		); err != nil {
			return nil, err
		}
		message.CreatedAt = message.CreatedAt.UTC()
		message.Unread = MessageIsUnread(message.Author, readAt.Time)
		messages = append(messages, message)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
	if err = service.attachMessageFiles(ctx, messages); err != nil {
		return nil, err
	}
	return messages, nil
}

// CreateTicket opens a conversation with its first message.
func (service *Service) CreateTicket(ctx context.Context, input NewTicket) (Conversation, error) {
	subject := truncateRunes(strings.TrimSpace(input.Subject), MaxSubjectLength)
	body := strings.TrimSpace(input.Body)
	if body == "" {
		return Conversation{}, invalid("a support request needs a message")
	}
	if len([]rune(body)) > MaxMessageLength {
		return Conversation{}, invalid("a support message can hold at most 4000 characters")
	}
	if subject == "" {
		subject = truncateRunes(firstLine(body), MaxSubjectLength)
	}

	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Conversation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	key := strings.TrimSpace(input.IdempotencyKey)
	if key != "" {
		// The dedupe key is unique per ticket, which cannot deduplicate the
		// creation of the ticket itself. A transaction-scoped advisory lock on
		// the customer and the key closes that gap: two submissions racing on one
		// key serialise, and the second finds the first's ticket.
		if _, err = tx.Exec(ctx,
			`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
			input.CustomerID+"|"+key); err != nil {
			return Conversation{}, err
		}
		var existing string
		err = tx.QueryRow(ctx, `SELECT t.id::text
			FROM support_messages m
			JOIN support_tickets t ON t.id = m.ticket_id
			WHERE t.user_id = $1::uuid AND m.dedupe_key = $2
			ORDER BY m.id LIMIT 1`, input.CustomerID, key).Scan(&existing)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Conversation{}, err
		}
		if err == nil {
			// Committing rather than rolling back releases the advisory lock the
			// normal way; nothing was written, so the commit is empty.
			if err = tx.Commit(ctx); err != nil {
				return Conversation{}, err
			}
			return service.Conversation(ctx, input.CustomerID, existing)
		}
	}

	var open int
	if err = tx.QueryRow(ctx, `SELECT count(*)::integer FROM support_tickets
		WHERE user_id = $1::uuid AND status IN ('open', 'pending')`,
		input.CustomerID).Scan(&open); err != nil {
		return Conversation{}, err
	}
	if open >= service.limits.MaxOpenTickets {
		return Conversation{}, ErrTooManyOpenTickets
	}

	var ticketID string
	err = tx.QueryRow(ctx, `INSERT INTO support_tickets (user_id, subject, queue_id)
		SELECT $1::uuid, $2, q.id
		FROM support_queues q WHERE q.is_default AND q.archived_at IS NULL
		RETURNING id::text`, input.CustomerID, subject).Scan(&ticketID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrQueueMissing
	}
	if err != nil {
		return Conversation{}, err
	}
	if _, err = appendCustomerMessage(ctx, tx, ticketID, body, key); err != nil {
		return Conversation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Conversation{}, err
	}
	return service.Conversation(ctx, input.CustomerID, ticketID)
}

// Reply appends a customer message to an existing conversation.
func (service *Service) Reply(ctx context.Context, input NewMessage) (Conversation, error) {
	body := strings.TrimSpace(input.Body)
	if body == "" {
		return Conversation{}, invalid("a reply needs a message")
	}
	if len([]rune(body)) > MaxMessageLength {
		return Conversation{}, invalid("a support message can hold at most 4000 characters")
	}
	if !looksLikeUUID(strings.TrimSpace(input.TicketID)) {
		return Conversation{}, ErrNotFound
	}

	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Conversation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockWritableTicket(ctx, tx, input.CustomerID, input.TicketID); err != nil {
		return Conversation{}, err
	}
	if _, err = appendCustomerMessage(
		ctx, tx, input.TicketID, body, strings.TrimSpace(input.IdempotencyKey),
	); err != nil {
		return Conversation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Conversation{}, err
	}
	return service.Conversation(ctx, input.CustomerID, input.TicketID)
}

// lockWritableTicket takes the ticket row and reports whether the customer may
// write into it, so the status cannot change between the check and the insert.
func lockWritableTicket(ctx context.Context, tx pgx.Tx, customerID, ticketID string) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM support_tickets
		WHERE id = $2::uuid AND user_id = $1::uuid FOR UPDATE`, customerID, ticketID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !TicketAcceptsReply(status) {
		return ErrTicketClosed
	}
	return nil
}

// appendCustomerMessage writes one customer turn, moves the ticket's counters
// with it, and returns the message it wrote.
//
// The operator-side unread counter is raised only when a row is actually
// inserted, so a replayed submission does not make the queue show work that has
// not arrived. A message on a resolved ticket reopens it, because a customer who
// writes back has not had their question answered, and a message on a pending
// ticket — one waiting on the customer — moves it back to open, because the
// wait is over. The bot applies the same rules, so the two surfaces cannot leave
// a ticket in different states.
func appendCustomerMessage(
	ctx context.Context, tx pgx.Tx, ticketID, body, dedupeKey string,
) (int64, error) {
	var messageID int64
	err := tx.QueryRow(ctx, `INSERT INTO support_messages (ticket_id, sender, body, dedupe_key)
		VALUES ($1::uuid, 'customer', $2, NULLIF($3, ''))
		ON CONFLICT (ticket_id, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING
		RETURNING id`, ticketID, body, dedupeKey).Scan(&messageID)
	if errors.Is(err, pgx.ErrNoRows) {
		// The key has already been used on this ticket, so the message the
		// customer meant to send is already there. Its identifier is read back
		// rather than invented, because an upload hangs a file on it.
		err = tx.QueryRow(ctx, `SELECT id FROM support_messages
			WHERE ticket_id = $1::uuid AND dedupe_key = $2`, ticketID, dedupeKey).Scan(&messageID)
		return messageID, err
	}
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, customerMessageTicketUpdate, ticketID, firstLine(body)); err != nil {
		return 0, err
	}
	return messageID, nil
}

// customerMessageTicketUpdate is the ticket transition a customer message
// causes. It is one statement, shared by the bot through the same wording, so
// the two surfaces cannot drift: resolved and pending both return to open, only
// a resolved one counts as a reopen and clears its resolution stamp.
const customerMessageTicketUpdate = `UPDATE support_tickets
	SET updated_at = now(), last_message_at = now(),
	    operator_unread_count = operator_unread_count + 1,
	    status = CASE WHEN status IN ('resolved', 'pending') THEN 'open' ELSE status END,
	    reopened_count = CASE WHEN status = 'resolved' THEN reopened_count + 1 ELSE reopened_count END,
	    resolved_at = CASE WHEN status = 'resolved' THEN NULL ELSE resolved_at END,
	    subject = CASE WHEN subject = '' THEN left($2, 120) ELSE subject END
	WHERE id = $1::uuid`

// MarkRead clears the customer's unread state for one conversation.
//
// It writes the message's own read stamp and the ticket's counter — the columns
// the bot writes when the same customer opens the same conversation in Telegram.
// Recording read state per surface would mean a reply read in a chat is still
// bold in the browser, which is the defect this shares a column to avoid.
func (service *Service) MarkRead(ctx context.Context, customerID, ticketID string) (Ticket, error) {
	if !looksLikeUUID(strings.TrimSpace(ticketID)) {
		return Ticket{}, ErrNotFound
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Ticket{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := tx.Exec(ctx, `UPDATE support_tickets SET customer_unread_count = 0
		WHERE id = $2::uuid AND user_id = $1::uuid`, customerID, ticketID)
	if err != nil {
		return Ticket{}, err
	}
	if result.RowsAffected() == 0 {
		return Ticket{}, ErrNotFound
	}
	if _, err = tx.Exec(ctx, `UPDATE support_messages SET read_at = now()
		WHERE ticket_id = $1::uuid AND sender <> 'customer' AND read_at IS NULL`,
		ticketID); err != nil {
		return Ticket{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Ticket{}, err
	}
	return service.ticket(ctx, customerID, ticketID)
}

// Close ends a conversation at the customer's request.
//
// Closing an already-closed ticket succeeds and changes nothing. The customer
// asked for a state, not for a transition, and answering an error to somebody
// who already has what they asked for is a worse outcome than a no-op.
func (service *Service) Close(ctx context.Context, customerID, ticketID string) (Ticket, error) {
	return service.setStatus(ctx, customerID, ticketID, StatusClosed)
}

// Reopen returns a closed or resolved conversation to the operator queue.
func (service *Service) Reopen(ctx context.Context, customerID, ticketID string) (Ticket, error) {
	return service.setStatus(ctx, customerID, ticketID, StatusOpen)
}

func (service *Service) setStatus(
	ctx context.Context, customerID, ticketID, target string,
) (Ticket, error) {
	if !looksLikeUUID(strings.TrimSpace(ticketID)) {
		return Ticket{}, ErrNotFound
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Ticket{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM support_tickets
		WHERE id = $2::uuid AND user_id = $1::uuid FOR UPDATE`, customerID, ticketID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Ticket{}, ErrNotFound
	}
	if err != nil {
		return Ticket{}, err
	}
	if status == StatusMerged {
		// A merged ticket is a pointer to another conversation. Changing its
		// status would detach the trail that explains where the customer's words
		// went, so the panel is told to follow the pointer instead.
		return Ticket{}, invalid("this conversation was merged into another one")
	}
	switch {
	case target == StatusClosed && status != StatusClosed:
		_, err = tx.Exec(ctx, `UPDATE support_tickets
			SET status = 'closed', closed_at = now(), updated_at = now()
			WHERE id = $1::uuid`, ticketID)
	case target == StatusOpen && (status == StatusClosed || status == StatusResolved):
		// Reopening counts, because a ticket that keeps coming back is the signal
		// the support report is built to surface.
		_, err = tx.Exec(ctx, `UPDATE support_tickets
			SET status = 'open', closed_at = NULL, resolved_at = NULL,
			    reopened_count = reopened_count + 1, updated_at = now(),
			    operator_unread_count = operator_unread_count + 1
			WHERE id = $1::uuid`, ticketID)
	}
	if err != nil {
		return Ticket{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Ticket{}, err
	}
	return service.ticket(ctx, customerID, ticketID)
}

// ticket reads one summary the customer owns.
func (service *Service) ticket(ctx context.Context, customerID, ticketID string) (Ticket, error) {
	ticket, err := scanTicket(service.pool.QueryRow(ctx, `SELECT `+ticketColumns+`
		FROM support_tickets t WHERE t.id = $2::uuid AND t.user_id = $1::uuid`,
		customerID, ticketID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Ticket{}, ErrNotFound
	}
	return ticket, err
}
