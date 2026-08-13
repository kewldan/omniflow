package botapp

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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
	ErrTicketNotFound   = errors.New("support ticket not found")
	ErrTicketClosed     = errors.New("support ticket is closed")
	ErrAttachmentTooBig = errors.New("attachment is larger than the 10 MB limit")
	ErrAttachmentKind   = errors.New("only photos and documents can be attached")
)

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
		if status == "closed" {
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
	// writes back has not had their question answered.
	if _, err = tx.Exec(ctx, `UPDATE support_tickets
		SET updated_at = now(), last_message_at = now(),
		    operator_unread_count = operator_unread_count + 1,
		    status = CASE WHEN status = 'resolved' THEN 'open' ELSE status END,
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
func (store *PostgresStore) SetTicketStatus(ctx context.Context, customerID, ticketID, status string) error {
	if status != "open" && status != "closed" {
		return errors.New("unsupported ticket status")
	}
	closedAt := pgtype.Timestamptz{}
	if status == "closed" {
		closedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	result, err := store.pool.Exec(ctx, `UPDATE support_tickets
		SET status = $3, closed_at = $4, updated_at = now()
		WHERE id = $2::uuid AND user_id = $1::uuid AND status <> $3`, customerID, ticketID, status, closedAt)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrTicketNotFound
	}
	return nil
}

// PendingOperatorReply is one operator message that still has to reach Telegram.
type PendingOperatorReply struct {
	MessageID  int64
	TicketID   string
	CustomerID string
	TelegramID int64
	Subject    string
	Body       string
	Locale     string
	CreatedAt  time.Time
}

// PendingOperatorReplies claims operator messages that have not been delivered.
// Delivery is deduplicated by the message identifier, so a retried run cannot
// send the same reply twice.
func (store *PostgresStore) PendingOperatorReplies(ctx context.Context, limit int) ([]PendingOperatorReply, error) {
	rows, err := store.pool.Query(ctx, telegramRecipients+`
		SELECT DISTINCT ON (m.id) m.id, t.id::text, t.user_id::text, recipient.telegram_id, t.subject, m.body,
		CASE WHEN COALESCE(p.locale, 'auto') = 'auto' THEN u.locale ELSE p.locale END, m.created_at
		FROM support_messages m
		JOIN support_tickets t ON t.id = m.ticket_id
		JOIN users u ON u.id = t.user_id
		JOIN recipient ON recipient.user_id = t.user_id
		LEFT JOIN bot_preferences p ON p.user_id = t.user_id
		WHERE m.sender = 'operator' AND m.delivered_at IS NULL AND u.status = 'active'
		ORDER BY m.id, m.created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	replies := make([]PendingOperatorReply, 0, limit)
	for rows.Next() {
		var reply PendingOperatorReply
		if err := rows.Scan(&reply.MessageID, &reply.TicketID, &reply.CustomerID, &reply.TelegramID,
			&reply.Subject, &reply.Body, &reply.Locale, &reply.CreatedAt); err != nil {
			return nil, err
		}
		replies = append(replies, reply)
	}
	return replies, rows.Err()
}

// MarkOperatorReplyDelivered records a successful delivery and raises the
// customer's unread counter exactly once per message.
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
	return tx.Commit(ctx)
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
