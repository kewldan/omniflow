package botapp

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	// maxSupportBody matches the support_messages length constraint.
	maxSupportBody = 4000
	// maxAttachmentBytes matches the support_attachments size constraint.
	maxAttachmentBytes = 10 * 1024 * 1024
	maxAttachments     = 5
)

var (
	ErrTicketNotFound = errors.New("support ticket not found")
	ErrTicketClosed   = errors.New("support ticket is closed")
	// ErrTicketMerged reports a write to a conversation an operator folded into
	// another one. The customer's words moved there, so the right answer is a
	// pointer rather than a refusal dressed as "closed".
	ErrTicketMerged     = errors.New("support ticket was merged into another conversation")
	ErrAttachmentTooBig = errors.New("attachment is larger than the 10 MB limit")
	ErrAttachmentKind   = errors.New("only photos and documents can be attached")
)

// ticketAcceptsReply mirrors the web panel's rule: open, pending, and resolved
// conversations take a customer message; a closed one needs an explicit reopen
// and a merged one continued somewhere else.
func ticketAcceptsReply(status string) bool {
	return status == "open" || status == "pending" || status == "resolved"
}

// ticketCanReopen mirrors the web panel's reopen rule: only a closed or
// resolved conversation has anything to reopen.
func ticketCanReopen(status string) bool {
	return status == "closed" || status == "resolved"
}

// Ticket is one customer support conversation.
type Ticket struct {
	ID            string
	Subject       string
	Status        string
	Priority      string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastMessageAt time.Time
	UnreadCount   int
	MessageCount  int
}

// TicketMessage is one message in a conversation, with its attachments.
type TicketMessage struct {
	ID          int64
	Sender      string
	Body        string
	CreatedAt   time.Time
	ReadAt      time.Time
	Attachments []Attachment
}

// Attachment is a Telegram file reference plus safe metadata. Omniflow never
// stores the file itself.
type Attachment struct {
	Kind           string
	TelegramFileID string
	FileName       string
	MimeType       string
	SizeBytes      int64
}

// Validate applies the size and type restrictions before anything is persisted.
func (attachment Attachment) Validate() error {
	if attachment.Kind != "photo" && attachment.Kind != "document" {
		return ErrAttachmentKind
	}
	if attachment.SizeBytes <= 0 || attachment.SizeBytes > maxAttachmentBytes {
		return ErrAttachmentTooBig
	}
	return nil
}

// Tickets lists the customer's conversations, newest activity first.
func (store *PostgresStore) Tickets(ctx context.Context, customerID string, limit int) ([]Ticket, error) {
	rows, err := store.pool.Query(ctx, `SELECT t.id::text, t.subject, t.status, t.priority, t.created_at,
		t.updated_at, t.last_message_at, t.customer_unread_count,
		(SELECT count(*) FROM support_messages m WHERE m.ticket_id = t.id)
		FROM support_tickets t WHERE t.user_id = $1::uuid
		ORDER BY t.updated_at DESC LIMIT $2`, customerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tickets := make([]Ticket, 0, limit)
	for rows.Next() {
		var ticket Ticket
		var messageCount int64
		if err := rows.Scan(&ticket.ID, &ticket.Subject, &ticket.Status, &ticket.Priority, &ticket.CreatedAt,
			&ticket.UpdatedAt, &ticket.LastMessageAt, &ticket.UnreadCount, &messageCount); err != nil {
			return nil, err
		}
		ticket.MessageCount = int(messageCount)
		tickets = append(tickets, ticket)
	}
	return tickets, rows.Err()
}

// UnreadSupportCount reports how many operator replies the customer has not read.
func (store *PostgresStore) UnreadSupportCount(ctx context.Context, customerID string) (int, error) {
	var unread int
	err := store.pool.QueryRow(ctx, `SELECT COALESCE(sum(customer_unread_count), 0)::integer
		FROM support_tickets WHERE user_id = $1::uuid`, customerID).Scan(&unread)
	return unread, err
}

// Ticket reads one conversation that belongs to the customer.
func (store *PostgresStore) Ticket(ctx context.Context, customerID, ticketID string) (Ticket, []TicketMessage, error) {
	var ticket Ticket
	var messageCount int64
	err := store.pool.QueryRow(ctx, `SELECT t.id::text, t.subject, t.status, t.priority, t.created_at,
		t.updated_at, t.last_message_at, t.customer_unread_count,
		(SELECT count(*) FROM support_messages m WHERE m.ticket_id = t.id)
		FROM support_tickets t WHERE t.id = $2::uuid AND t.user_id = $1::uuid`, customerID, ticketID).
		Scan(&ticket.ID, &ticket.Subject, &ticket.Status, &ticket.Priority, &ticket.CreatedAt,
			&ticket.UpdatedAt, &ticket.LastMessageAt, &ticket.UnreadCount, &messageCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return Ticket{}, nil, ErrTicketNotFound
	}
	if err != nil {
		return Ticket{}, nil, err
	}
	ticket.MessageCount = int(messageCount)
	rows, err := store.pool.Query(ctx, `SELECT m.id, m.sender, m.body, m.created_at, m.read_at
		FROM support_messages m WHERE m.ticket_id = $1::uuid
		ORDER BY m.created_at DESC LIMIT 20`, ticketID)
	if err != nil {
		return Ticket{}, nil, err
	}
	defer rows.Close()
	messages := make([]TicketMessage, 0, 20)
	for rows.Next() {
		var message TicketMessage
		var readAt pgtype.Timestamptz
		if err := rows.Scan(&message.ID, &message.Sender, &message.Body, &message.CreatedAt, &readAt); err != nil {
			return Ticket{}, nil, err
		}
		message.ReadAt = readAt.Time
		messages = append(messages, message)
	}
	if err = rows.Err(); err != nil {
		return Ticket{}, nil, err
	}
	if err = store.attachMessageFiles(ctx, messages); err != nil {
		return Ticket{}, nil, err
	}
	// Oldest first reads naturally as a conversation.
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
	return ticket, messages, nil
}

func (store *PostgresStore) attachMessageFiles(ctx context.Context, messages []TicketMessage) error {
	if len(messages) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	// A web upload has no Telegram identifier, and the conversation view needs
	// none: it renders a name and a size. Coalescing rather than selecting the
	// column away keeps the field meaning what it says — empty for a file
	// Telegram has never seen — instead of carrying a value that would fail on
	// its way back there.
	rows, err := store.pool.Query(ctx, `SELECT message_id, kind, COALESCE(telegram_file_id, ''),
		COALESCE(file_name, ''), COALESCE(mime_type, ''), size_bytes FROM support_attachments
		WHERE message_id = ANY($1) AND retain_until > now() ORDER BY created_at`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	byMessage := make(map[int64][]Attachment, len(messages))
	for rows.Next() {
		var messageID int64
		var attachment Attachment
		if err := rows.Scan(&messageID, &attachment.Kind, &attachment.TelegramFileID, &attachment.FileName,
			&attachment.MimeType, &attachment.SizeBytes); err != nil {
			return err
		}
		byMessage[messageID] = append(byMessage[messageID], attachment)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for index := range messages {
		messages[index].Attachments = byMessage[messages[index].ID]
	}
	return nil
}

// AppendCustomerMessage opens or continues a conversation. A ticket identifier
// of zero length starts a new one; the schema keeps at most one open ticket per
// customer, so a second "new request" continues the open conversation instead of
// fragmenting the history.
func (store *PostgresStore) AppendCustomerMessage(ctx context.Context, customerID, ticketID, subject, body string, telegramMessageID int, attachments []Attachment) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" && len(attachments) == 0 {
		return "", errors.New("a support message needs text or an attachment")
	}
	if len([]rune(body)) > maxSupportBody {
		return "", errors.New("support message must contain 1 to 4000 characters")
	}
	if body == "" {
		body = "[attachment]"
	}
	if len(attachments) > maxAttachments {
		return "", errors.New("at most five attachments can be sent at once")
	}
	for _, attachment := range attachments {
		if err := attachment.Validate(); err != nil {
			return "", err
		}
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var resolved string
	if ticketID != "" {
		var status string
		err = tx.QueryRow(ctx, `SELECT id::text, status FROM support_tickets
			WHERE id = $2::uuid AND user_id = $1::uuid FOR UPDATE`, customerID, ticketID).Scan(&resolved, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrTicketNotFound
		}
		if err != nil {
			return "", err
		}
		switch {
		case status == "merged":
			return "", ErrTicketMerged
		case !ticketAcceptsReply(status):
			return "", ErrTicketClosed
		}
	} else {
		// A customer may now hold several open tickets, because a billing
		// question and a connection problem are two conversations. The bot still
		// continues the most recent open one when the customer just types,
		// because that is what they mean; the panel and the ticket list are
		// where a second thread is chosen deliberately.
		err = tx.QueryRow(ctx, `
			WITH existing AS (
				SELECT id FROM support_tickets
				WHERE user_id = $1::uuid AND status IN ('open', 'pending')
				ORDER BY last_message_at DESC LIMIT 1
			), created AS (
				INSERT INTO support_tickets (user_id, subject, queue_id)
				SELECT $1::uuid, left($2, 120),
				       (SELECT id FROM support_queues WHERE is_default AND archived_at IS NULL)
				WHERE NOT EXISTS (SELECT 1 FROM existing)
				RETURNING id
			)
			SELECT id::text FROM existing
			UNION ALL
			SELECT id::text FROM created`, customerID, subject).Scan(&resolved)
		if err != nil {
			return "", err
		}
	}
	var messageID int64
	if err = tx.QueryRow(ctx, `INSERT INTO support_messages (ticket_id, sender, body, telegram_message_id)
		VALUES ($1::uuid, 'customer', $2, $3) RETURNING id`, resolved, body, telegramMessageID).Scan(&messageID); err != nil {
		return "", err
	}
	for _, attachment := range attachments {
		if _, err = tx.Exec(ctx, `INSERT INTO support_attachments
			(message_id, kind, origin, telegram_file_id, file_name, mime_type, size_bytes)
			VALUES ($1, $2, 'telegram', $3, NULLIF($4, ''), NULLIF($5, ''), $6)
			ON CONFLICT (message_id, telegram_file_id) DO NOTHING`,
			messageID, attachment.Kind, attachment.TelegramFileID, attachment.FileName,
			attachment.MimeType, attachment.SizeBytes); err != nil {
			return "", err
		}
	}
	// The operator-side unread counter is the mirror of the customer's, and it
	// is what makes a queue show what has arrived since anybody looked. A
	// customer message on a resolved ticket reopens it, because a customer who
	// writes back has not had their question answered; one on a pending ticket
	// returns it to open, because the wait for the customer is over. The
	// statement is word for word the web panel's (accountsupport), so the two
	// surfaces cannot leave a ticket in different states.
	if _, err = tx.Exec(ctx, `UPDATE support_tickets
		SET updated_at = now(), last_message_at = now(),
		    operator_unread_count = operator_unread_count + 1,
		    status = CASE WHEN status IN ('resolved', 'pending') THEN 'open' ELSE status END,
		    reopened_count = CASE WHEN status = 'resolved' THEN reopened_count + 1 ELSE reopened_count END,
		    resolved_at = CASE WHEN status = 'resolved' THEN NULL ELSE resolved_at END,
		    subject = CASE WHEN subject = '' THEN left($2, 120) ELSE subject END
		WHERE id = $1::uuid`, resolved, firstLine(body)); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM bot_sessions WHERE telegram_id IN (
		SELECT (i.provider_subject)::bigint FROM identities i
		WHERE i.user_id = $1::uuid AND i.provider = 'telegram' AND i.status = 'active')`, customerID); err != nil {
		return "", err
	}
	return resolved, tx.Commit(ctx)
}

// MarkTicketRead clears the unread counter and stamps operator messages as read.
func (store *PostgresStore) MarkTicketRead(ctx context.Context, customerID, ticketID string) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `UPDATE support_messages SET read_at = now()
		WHERE ticket_id = $2::uuid AND sender <> 'customer' AND read_at IS NULL
		  AND EXISTS (SELECT 1 FROM support_tickets t WHERE t.id = $2::uuid AND t.user_id = $1::uuid)`,
		customerID, ticketID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE support_tickets SET customer_unread_count = 0
		WHERE id = $2::uuid AND user_id = $1::uuid`, customerID, ticketID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetTicketStatus closes or reopens a conversation the customer owns.
//
// It applies the same transitions the web panel does, so a ticket cannot be
// left in a different state depending on which surface the customer used.
// Closing an already-closed ticket and reopening one that is already open
// succeed and change nothing: the customer asked for a state, not a transition.
// A merged ticket refuses both, because its conversation continued elsewhere.
func (store *PostgresStore) SetTicketStatus(ctx context.Context, customerID, ticketID, status string) error {
	if status != "open" && status != "closed" {
		return errors.New("unsupported ticket status")
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var current string
	err = tx.QueryRow(ctx, `SELECT status FROM support_tickets
		WHERE id = $2::uuid AND user_id = $1::uuid FOR UPDATE`, customerID, ticketID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTicketNotFound
	}
	if err != nil {
		return err
	}
	if current == "merged" {
		return ErrTicketMerged
	}
	switch {
	case status == "closed" && current != "closed":
		_, err = tx.Exec(ctx, `UPDATE support_tickets
			SET status = 'closed', closed_at = now(), updated_at = now()
			WHERE id = $1::uuid`, ticketID)
	case status == "open" && ticketCanReopen(current):
		// Reopening counts, and it puts the ticket back in front of an operator:
		// a conversation that keeps coming back is the signal the support report
		// is built to surface, and a reopen nobody sees is not a reopen.
		_, err = tx.Exec(ctx, `UPDATE support_tickets
			SET status = 'open', closed_at = NULL, resolved_at = NULL,
			    reopened_count = reopened_count + 1, updated_at = now(),
			    operator_unread_count = operator_unread_count + 1
			WHERE id = $1::uuid`, ticketID)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PendingOperatorReply is one operator or system message that still has to
// reach Telegram. TelegramID is zero when the customer has no Telegram identity
// at all, which the delivery pass records rather than skips.
type PendingOperatorReply struct {
	MessageID  int64
	TicketID   string
	CustomerID string
	TelegramID int64
	Subject    string
	Sender     string
	Body       string
	Locale     string
	CreatedAt  time.Time
}

// supportDeliveryRetries is how many transport failures a support reply
// survives before it is left alone. It matches the policy machinery's limit so
// the two histories read the same way.
const supportDeliveryRetries = 3

// supportDeliveryKey is the dedupe key of the delivery row that records what
// happened to one support message. The row is what makes the customer's
// notification history show the reply, and its terminal states are what keep
// an undeliverable message from holding the head of the queue forever.
const supportDeliveryKey = `'support-message:' || m.id::text`

// PendingOperatorReplies reads the messages that still have to be pushed.
//
// A message leaves this set in exactly one of three ways: it was delivered
// (`delivered_at`), it was recorded as undeliverable (a `suppressed` delivery
// row, written when the customer has blocked the bot, deleted their account, or
// has no Telegram identity), or it failed its last retry (a `failed` row at the
// retry limit). Without the last two, one customer who blocked the bot would
// sit at the head of the queue and the replies behind them would never move.
func (store *PostgresStore) PendingOperatorReplies(ctx context.Context, limit int) ([]PendingOperatorReply, error) {
	rows, err := store.pool.Query(ctx, telegramRecipients+`
		SELECT DISTINCT ON (m.id) m.id, t.id::text, t.user_id::text, COALESCE(recipient.telegram_id, 0),
		t.subject, m.sender, m.body,
		CASE WHEN COALESCE(p.locale, 'auto') = 'auto' THEN u.locale ELSE p.locale END, m.created_at
		FROM support_messages m
		JOIN support_tickets t ON t.id = m.ticket_id
		JOIN users u ON u.id = t.user_id
		LEFT JOIN recipient ON recipient.user_id = t.user_id
		LEFT JOIN bot_preferences p ON p.user_id = t.user_id
		WHERE m.sender IN ('operator', 'system') AND m.delivered_at IS NULL AND u.status = 'active'
		  AND NOT EXISTS (
			SELECT 1 FROM notification_deliveries n
			WHERE n.user_id = t.user_id AND n.kind = 'support' AND n.subscription_id IS NULL
			  AND n.dedupe_key = `+supportDeliveryKey+`
			  AND (n.status IN ('sent', 'suppressed')
			       OR (n.status = 'failed' AND n.failure_count >= $2)))
		ORDER BY m.id, recipient.telegram_id LIMIT $1`, limit, supportDeliveryRetries)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	replies := make([]PendingOperatorReply, 0, limit)
	for rows.Next() {
		var reply PendingOperatorReply
		if err := rows.Scan(&reply.MessageID, &reply.TicketID, &reply.CustomerID, &reply.TelegramID,
			&reply.Subject, &reply.Sender, &reply.Body, &reply.Locale, &reply.CreatedAt); err != nil {
			return nil, err
		}
		replies = append(replies, reply)
	}
	return replies, rows.Err()
}

// MarkOperatorReplyDelivered records a successful delivery, raises the
// customer's unread counter exactly once per message, and writes the delivery
// row the customer's notification history reads.
func (store *PostgresStore) MarkOperatorReplyDelivered(ctx context.Context, messageID int64) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := tx.Exec(ctx, `UPDATE support_messages SET delivered_at = now()
		WHERE id = $1 AND delivered_at IS NULL`, messageID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE support_tickets
		SET customer_unread_count = customer_unread_count + 1, updated_at = now(), last_message_at = now()
		WHERE id = (SELECT ticket_id FROM support_messages WHERE id = $1)`, messageID); err != nil {
		return err
	}
	if err = recordSupportDelivery(ctx, tx, messageID, "sent", ""); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkOperatorReplyUndeliverable records that a message can never be pushed to
// this customer: the bot is blocked, the account is gone, or there is no
// Telegram identity to push to. The message stays unread in the web panel and
// the ticket keeps its state; only the push is given up on.
func (store *PostgresStore) MarkOperatorReplyUndeliverable(ctx context.Context, messageID int64, code string) error {
	return recordSupportDelivery(ctx, store.pool, messageID, "suppressed", code)
}

// MarkOperatorReplyFailed counts one transport failure. The message is retried
// on a later pass until it has failed supportDeliveryRetries times.
func (store *PostgresStore) MarkOperatorReplyFailed(ctx context.Context, messageID int64, code string) error {
	return recordSupportDelivery(ctx, store.pool, messageID, "failed", code)
}

type execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// recordSupportDelivery upserts the delivery row for one support message. The
// customer is resolved through the ticket so a caller cannot file a delivery
// against somebody else's history.
func recordSupportDelivery(ctx context.Context, db execer, messageID int64, status, code string) error {
	_, err := db.Exec(ctx, `INSERT INTO notification_deliveries
		(user_id, kind, dedupe_key, class, status, error_code, sent_at, failure_count)
		SELECT t.user_id, 'support', `+supportDeliveryKey+`, 'transactional', $2, NULLIF($3, ''),
		       CASE WHEN $2 = 'sent' THEN now() END,
		       CASE WHEN $2 = 'failed' THEN 1 ELSE 0 END
		FROM support_messages m
		JOIN support_tickets t ON t.id = m.ticket_id
		WHERE m.id = $1
		ON CONFLICT (user_id, kind, subscription_id, dedupe_key) DO UPDATE
		SET status = EXCLUDED.status, error_code = EXCLUDED.error_code,
		    sent_at = COALESCE(EXCLUDED.sent_at, notification_deliveries.sent_at),
		    failure_count = notification_deliveries.failure_count + EXCLUDED.failure_count`,
		messageID, status, code)
	return err
}

// Attachment retention is not here. It was, back when only the reference was
// stored and deleting the row was the whole policy; a web upload broke that,
// because this installation holds those bytes and a row deleted without them
// leaves a file nothing will ever reclaim.
//
// `retention.Worker` owns it now, through `accountsupport.SweepExpiredAttachments`,
// which deletes on the same schedule and then removes the files no surviving row
// still references. Two deleters would race for no benefit, and the one that
// won would decide whether the disk was reclaimed.

func firstLine(body string) string {
	line := body
	if index := strings.IndexAny(line, "\r\n"); index >= 0 {
		line = line[:index]
	}
	return strings.TrimSpace(line)
}
