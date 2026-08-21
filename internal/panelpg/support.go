package panelpg

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// SupportQueue is one named bucket of work with its promise attached.
type SupportQueue struct {
	ID                   string `json:"id"`
	Code                 string `json:"code"`
	NameEN               string `json:"nameEn"`
	NameRU               string `json:"nameRu"`
	FirstResponseSeconds int64  `json:"firstResponseTargetSeconds"`
	ResolutionSeconds    int64  `json:"resolutionTargetSeconds"`
	IsDefault            bool   `json:"isDefault"`
	SortOrder            int32  `json:"sortOrder"`
	// The three counts are what an operator reads before choosing what to work
	// on: how much is here, how much nobody owns, and how much is already late.
	Open       int64 `json:"openCount"`
	Unassigned int64 `json:"unassignedCount"`
	Breached   int64 `json:"breachedCount"`
}

// SupportTicket is one conversation as the desk sees it.
type SupportTicket struct {
	ID           string     `json:"id"`
	CustomerID   string     `json:"customerId"`
	QueueID      string     `json:"queueId"`
	QueueCode    string     `json:"queueCode"`
	Subject      string     `json:"subject"`
	Status       string     `json:"status"`
	Priority     string     `json:"priority"`
	AssigneeID   string     `json:"assigneeId,omitempty"`
	AssigneeName string     `json:"assigneeName,omitempty"`
	Tags         []string   `json:"tags"`
	MessageCount int64      `json:"messageCount"`
	Unread       int32      `json:"unreadCount"`
	Reopened     int32      `json:"reopenedCount"`
	Breached     bool       `json:"firstResponseBreached"`
	MergedInto   string     `json:"mergedIntoTicketId,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastMessage  time.Time  `json:"lastMessageAt"`
	FirstReply   *time.Time `json:"firstResponseAt,omitempty"`
	ResolvedAt   *time.Time `json:"resolvedAt,omitempty"`
}

// SupportMessage is one turn of the conversation the customer can see.
type SupportMessage struct {
	ID         int64  `json:"id"`
	Sender     string `json:"sender"`
	Body       string `json:"body"`
	AuthorName string `json:"authorName,omitempty"`
	Delivered  bool   `json:"delivered"`
	// Delivery is the push outcome for an operator or system message: `queued`,
	// `retrying`, `delivered`, `undeliverable`, or `failed`. It is empty for a
	// customer message, which is never pushed anywhere. `undeliverable` is the
	// state the desk most needs to see — it means the customer will only read
	// this in the web panel, and `queued` would have said "any minute now".
	Delivery string `json:"delivery,omitempty"`
	// DeliveryReason is the classified code behind an undeliverable or failed
	// push: `bot_blocked`, `user_deactivated`, `chat_not_found`, `no_telegram`,
	// or a transport code.
	DeliveryReason string              `json:"deliveryReason,omitempty"`
	CreatedAt      time.Time           `json:"createdAt"`
	Attachments    []SupportAttachment `json:"attachments"`
}

// SupportAttachment is the metadata of one file on a conversation, as the desk
// sees it. There is no path, bucket, or Telegram file identifier in it: the
// download route resolves those from the identifier, behind the permission.
type SupportAttachment struct {
	ID        string `json:"id"`
	MessageID int64  `json:"messageId"`
	Kind      string `json:"kind"`
	FileName  string `json:"fileName"`
	MediaType string `json:"mediaType"`
	SizeBytes int64  `json:"sizeBytes"`
	// Origin is `web` for a file this installation holds and `telegram` for a
	// reference to a file Telegram holds. Only the former can be downloaded
	// here; the latter is read in the customer's chat.
	Origin       string    `json:"origin"`
	Downloadable bool      `json:"downloadable"`
	CreatedAt    time.Time `json:"createdAt"`
}

// attachmentOrigin maps the stored origin onto the word the desk uses. The
// table says `local`, which is true from the database's point of view and
// meaningless from an operator's.
func attachmentOrigin(stored string) string {
	if stored == "local" {
		return "web"
	}
	return stored
}

// supportDeliveryRetries mirrors the bot's retry limit for a support push. A
// message that has failed this many times is no longer tried.
const supportDeliveryRetries = 3

// deliveryState reduces the message stamp and the delivery row to one word the
// desk can show. The stamp wins: a message Telegram accepted is delivered
// whatever the row says, because the stamp is written in the same transaction
// as the row and is what raised the customer's unread counter.
func deliveryState(sender string, delivered bool, status string, failures int32) string {
	if sender == "customer" {
		return ""
	}
	switch {
	case delivered || status == "sent":
		return "delivered"
	case status == "suppressed":
		return "undeliverable"
	case status == "failed" && failures >= supportDeliveryRetries:
		return "failed"
	case status == "failed":
		return "retrying"
	default:
		return "queued"
	}
}

// SupportNote is an operator's private note.
//
// It is a distinct type reading a distinct table, not a flagged message. The
// separation is what makes it impossible to deliver: the path that sends a
// message to a customer reads `support_messages`, and a note is not in it.
type SupportNote struct {
	ID         int64     `json:"id"`
	AuthorName string    `json:"authorName"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"createdAt"`
}

// TicketFilter narrows the queue view.
type TicketFilter struct {
	QueueID        string
	Status         string
	Priority       string
	AssigneeID     string
	UnassignedOnly bool
	CustomerID     string
	Tag            string
	Cursor         string
	PageSize       int32
}

// TicketPage is one page of the queue.
type TicketPage struct {
	Items      []SupportTicket `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

// SupportQueues lists the queues with their live counts.
func (service *Service) SupportQueues(ctx context.Context) ([]SupportQueue, error) {
	rows, err := service.queries().ListSupportQueues(ctx)
	if err != nil {
		return nil, err
	}
	queues := make([]SupportQueue, 0, len(rows))
	for _, row := range rows {
		queues = append(queues, SupportQueue{
			ID: uuidString(row.SupportQueue.ID), Code: row.SupportQueue.Code,
			NameEN: row.SupportQueue.NameEn, NameRU: row.SupportQueue.NameRu,
			FirstResponseSeconds: row.SupportQueue.FirstResponseTargetSeconds,
			ResolutionSeconds:    row.SupportQueue.ResolutionTargetSeconds,
			IsDefault:            row.SupportQueue.IsDefault,
			SortOrder:            row.SupportQueue.SortOrder,
			Open:                 row.OpenCount,
			Unassigned:           row.UnassignedCount,
			Breached:             row.BreachedCount,
		})
	}
	return queues, nil
}

// SaveSupportQueue creates or updates a queue.
func (service *Service) SaveSupportQueue(
	ctx context.Context, queue SupportQueue, actor Actor,
) (SupportQueue, error) {
	if strings.TrimSpace(queue.Code) == "" ||
		strings.TrimSpace(queue.NameEN) == "" || strings.TrimSpace(queue.NameRU) == "" {
		return SupportQueue{}, ErrValidaton
	}
	if queue.FirstResponseSeconds < 0 || queue.ResolutionSeconds < 0 {
		return SupportQueue{}, ErrValidaton
	}

	var saved SupportQueue
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.UpsertSupportQueue(ctx, dbgen.UpsertSupportQueueParams{
			Code:   strings.ToLower(strings.TrimSpace(queue.Code)),
			NameEn: queue.NameEN, NameRu: queue.NameRU,
			FirstResponseTargetSeconds: queue.FirstResponseSeconds,
			ResolutionTargetSeconds:    queue.ResolutionSeconds,
			SortOrder:                  queue.SortOrder,
		})
		if txErr != nil {
			return txErr
		}
		if queue.IsDefault {
			// Clearing and setting run together, because the partial unique
			// index allows exactly one default and a gap would leave a new
			// ticket with nowhere to go.
			if txErr = queries.SetDefaultSupportQueue(ctx, row.ID); txErr != nil {
				return txErr
			}
			row.IsDefault = true
		}
		saved = SupportQueue{
			ID: uuidString(row.ID), Code: row.Code, NameEN: row.NameEn, NameRU: row.NameRu,
			FirstResponseSeconds: row.FirstResponseTargetSeconds,
			ResolutionSeconds:    row.ResolutionTargetSeconds,
			IsDefault:            row.IsDefault, SortOrder: row.SortOrder,
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.support_queue.saved", "configuration", "support_queue", saved.ID,
			map[string]any{
				"code": saved.Code, "isDefault": saved.IsDefault,
				"firstResponseTargetSeconds": saved.FirstResponseSeconds,
			},
		))
	})
	return saved, err
}

// SearchTickets reads the queue.
func (service *Service) SearchTickets(
	ctx context.Context, filter TicketFilter,
) (TicketPage, error) {
	size := pageSize(filter.PageSize)
	cursor := DecodeCursor(filter.Cursor)

	rows, err := service.queries().SearchSupportTickets(ctx, dbgen.SearchSupportTicketsParams{
		QueueID:             optionalUUID(filter.QueueID),
		Status:              optionalText(filter.Status),
		Priority:            optionalText(filter.Priority),
		AssigneeID:          optionalUUID(filter.AssigneeID),
		UnassignedOnly:      filter.UnassignedOnly,
		CustomerID:          optionalUUID(filter.CustomerID),
		Tag:                 optionalText(filter.Tag),
		CursorLastMessageAt: cursor.timestamp(),
		CursorID:            cursor.uuid(),
		PageSize:            size + 1,
	})
	if err != nil {
		return TicketPage{}, err
	}

	page := TicketPage{Items: make([]SupportTicket, 0, min(len(rows), int(size)))}
	for index, row := range rows {
		if index == int(size) {
			last := rows[index-1]
			page.NextCursor = EncodeCursor(
				timeValue(last.SupportTicket.LastMessageAt), uuidString(last.SupportTicket.ID),
			)
			break
		}
		ticket := ticketFrom(row.SupportTicket)
		ticket.QueueCode = row.QueueCode
		ticket.AssigneeName = row.AssigneeName
		ticket.Tags = row.Tags
		ticket.MessageCount = row.MessageCount
		ticket.Breached = row.FirstResponseBreached.Bool
		page.Items = append(page.Items, ticket)
	}
	return page, nil
}

// TicketDetail is one ticket with everything an operator needs to answer it.
type TicketDetail struct {
	Ticket   SupportTicket    `json:"ticket"`
	Messages []SupportMessage `json:"messages"`
	Notes    []SupportNote    `json:"notes"`
}

// Ticket assembles the conversation view.
//
// Messages and notes are returned as separate lists rather than interleaved.
// The panel renders them together, but keeping them apart in the response means
// no client can accidentally treat a note as something the customer has seen.
func (service *Service) Ticket(ctx context.Context, ticketID string) (TicketDetail, error) {
	id, err := parseUUID(ticketID)
	if err != nil {
		return TicketDetail{}, err
	}
	queries := service.queries()

	row, err := queries.GetSupportTicket(ctx, id)
	if err != nil {
		return TicketDetail{}, notFound(err)
	}
	ticket := ticketFrom(row.SupportTicket)
	ticket.QueueCode = row.QueueCode
	ticket.AssigneeName = row.AssigneeName
	ticket.Tags = row.Tags

	messageRows, err := queries.ListSupportMessages(ctx, dbgen.ListSupportMessagesParams{
		TicketID: id, PageSize: 200,
	})
	if err != nil {
		return TicketDetail{}, err
	}
	detail := TicketDetail{
		Ticket:   ticket,
		Messages: make([]SupportMessage, 0, len(messageRows)),
		Notes:    []SupportNote{},
	}
	for _, message := range messageRows {
		state := deliveryState(
			message.SupportMessage.Sender, message.SupportMessage.DeliveredAt.Valid,
			message.DeliveryStatus, message.DeliveryFailures,
		)
		reason := ""
		if state == "undeliverable" || state == "failed" || state == "retrying" {
			reason = message.DeliveryError
		}
		detail.Messages = append(detail.Messages, SupportMessage{
			ID: message.SupportMessage.ID, Sender: message.SupportMessage.Sender,
			Body: message.SupportMessage.Body, AuthorName: message.AuthorName,
			Delivered:      message.SupportMessage.DeliveredAt.Valid,
			Delivery:       state,
			DeliveryReason: reason,
			CreatedAt:      timeValue(message.SupportMessage.CreatedAt),
			Attachments:    []SupportAttachment{},
		})
	}

	// Attachments are read once for the whole conversation and hung on their
	// messages, so a ticket with forty screenshots costs one query, not forty.
	attachmentRows, err := queries.ListSupportTicketAttachments(ctx, id)
	if err != nil {
		return TicketDetail{}, err
	}
	byMessage := make(map[int64][]SupportAttachment, len(attachmentRows))
	for _, row := range attachmentRows {
		origin := attachmentOrigin(row.Origin)
		byMessage[row.MessageID] = append(byMessage[row.MessageID], SupportAttachment{
			ID: uuidString(row.ID), MessageID: row.MessageID, Kind: row.Kind,
			FileName: row.FileName, MediaType: row.MediaType, SizeBytes: row.SizeBytes,
			Origin: origin, Downloadable: origin == "web", CreatedAt: timeValue(row.CreatedAt),
		})
	}
	for index := range detail.Messages {
		if files, found := byMessage[detail.Messages[index].ID]; found {
			detail.Messages[index].Attachments = files
		}
	}

	noteRows, err := queries.ListSupportNotes(ctx, dbgen.ListSupportNotesParams{
		TicketID: id, PageSize: 200,
	})
	if err != nil {
		return TicketDetail{}, err
	}
	for _, note := range noteRows {
		detail.Notes = append(detail.Notes, SupportNote{
			ID: note.SupportNote.ID, AuthorName: note.AuthorName,
			Body: note.SupportNote.Body, CreatedAt: timeValue(note.SupportNote.CreatedAt),
		})
	}
	return detail, nil
}

// TicketAttachment resolves one file on a conversation for the download route,
// returning its metadata and — for a file this installation holds — the storage
// key that finds the bytes. The key is empty for a Telegram reference, and the
// caller answers that case without touching any store: Omniflow never fetches a
// customer's file from Telegram with the bot token on an operator's behalf.
func (service *Service) TicketAttachment(
	ctx context.Context, ticketID, attachmentID string,
) (SupportAttachment, string, error) {
	ticket, err := parseUUID(ticketID)
	if err != nil {
		return SupportAttachment{}, "", err
	}
	attachment, err := parseUUID(attachmentID)
	if err != nil {
		return SupportAttachment{}, "", err
	}
	row, err := service.queries().GetSupportTicketAttachment(ctx, dbgen.GetSupportTicketAttachmentParams{
		TicketID: ticket, AttachmentID: attachment,
	})
	if err != nil {
		return SupportAttachment{}, "", notFound(err)
	}
	origin := attachmentOrigin(row.Origin)
	meta := SupportAttachment{
		ID: uuidString(row.ID), MessageID: row.MessageID, Kind: row.Kind,
		FileName: row.FileName, MediaType: row.MediaType, SizeBytes: row.SizeBytes,
		Origin: origin, Downloadable: origin == "web", CreatedAt: timeValue(row.CreatedAt),
	}
	if !meta.Downloadable {
		return meta, "", nil
	}
	return meta, row.StorageKey, nil
}

// AssignTicket takes a ticket or puts it down.
func (service *Service) AssignTicket(
	ctx context.Context, ticketID, assigneeID string, actor Actor,
) (SupportTicket, error) {
	id, err := parseUUID(ticketID)
	if err != nil {
		return SupportTicket{}, err
	}
	var ticket SupportTicket
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.AssignSupportTicket(ctx, dbgen.AssignSupportTicketParams{
			TicketID: id, AssigneeID: optionalUUID(assigneeID),
		})
		if txErr != nil {
			return notFound(txErr)
		}
		ticket = ticketFrom(row)
		action := "panel.support_ticket.assigned"
		if assigneeID == "" {
			action = "panel.support_ticket.released"
		}
		return appendAudit(ctx, queries, actor.audit(
			action, "support", "support_ticket", ticketID,
			map[string]any{"assigneeId": assigneeID},
		))
	})
	return ticket, err
}

// MoveTicket sends a ticket to a different queue.
func (service *Service) MoveTicket(
	ctx context.Context, ticketID, queueID string, actor Actor,
) (SupportTicket, error) {
	id, err := parseUUID(ticketID)
	if err != nil {
		return SupportTicket{}, err
	}
	queue, err := parseUUID(queueID)
	if err != nil {
		return SupportTicket{}, err
	}
	var ticket SupportTicket
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.MoveSupportTicket(ctx, dbgen.MoveSupportTicketParams{
			TicketID: id, QueueID: queue,
		})
		if txErr != nil {
			return notFound(txErr)
		}
		ticket = ticketFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.support_ticket.moved", "support", "support_ticket", ticketID,
			map[string]any{"queueId": queueID},
		))
	})
	return ticket, err
}

// SetTicketPriority changes how a ticket compares to the work in front of it.
func (service *Service) SetTicketPriority(
	ctx context.Context, ticketID, priority string, actor Actor,
) (SupportTicket, error) {
	if !validTicketPriority[priority] {
		return SupportTicket{}, ErrValidaton
	}
	id, err := parseUUID(ticketID)
	if err != nil {
		return SupportTicket{}, err
	}
	var ticket SupportTicket
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.SetSupportTicketPriority(ctx, dbgen.SetSupportTicketPriorityParams{
			TicketID: id, Priority: priority,
		})
		if txErr != nil {
			return notFound(txErr)
		}
		ticket = ticketFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.support_ticket.prioritised", "support", "support_ticket", ticketID,
			map[string]any{"priority": priority},
		))
	})
	return ticket, err
}

// SetTicketStatus closes, resolves, or reopens a ticket.
func (service *Service) SetTicketStatus(
	ctx context.Context, ticketID, status string, actor Actor,
) (SupportTicket, error) {
	if !validTicketStatus[status] {
		return SupportTicket{}, ErrValidaton
	}
	id, err := parseUUID(ticketID)
	if err != nil {
		return SupportTicket{}, err
	}
	var ticket SupportTicket
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		before, txErr := queries.LockSupportTicket(ctx, id)
		if txErr != nil {
			return notFound(txErr)
		}
		row, txErr := queries.SetSupportTicketStatus(ctx, dbgen.SetSupportTicketStatusParams{
			TicketID: id, Status: status,
		})
		if txErr != nil {
			return notFound(txErr)
		}
		ticket = ticketFrom(row)
		// The customer is told when the desk finishes with their question. A
		// ticket that quietly turns resolved is a ticket the customer keeps
		// waiting on; the notice is a message, so it reaches them the same way
		// a reply does. It is written only on a real transition.
		if before.Status != status && (status == "resolved" || status == "closed") {
			if txErr = service.noteToCustomer(ctx, queries, id, status, ""); txErr != nil {
				return txErr
			}
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.support_ticket.status_changed", "support", "support_ticket", ticketID,
			map[string]any{"status": status, "reopenedCount": ticket.Reopened},
		))
	})
	return ticket, err
}

// noteToCustomer writes a system message in the customer's language about an
// event on the conversation and counts it as activity on the ticket. The bot's
// support loop delivers it like a reply; the web panel renders it as a system
// turn.
func (service *Service) noteToCustomer(
	ctx context.Context, queries *dbgen.Queries, ticketID pgtype.UUID, event, subject string,
) error {
	customer, err := queries.SupportCustomerLocale(ctx, ticketID)
	if err != nil {
		return notFound(err)
	}
	body := supportSystemNotice(customer.Locale, event, subject)
	if body == "" {
		return nil
	}
	if _, err = queries.AppendSupportSystemMessage(ctx, dbgen.AppendSupportSystemMessageParams{
		TicketID: ticketID, Body: body,
	}); err != nil {
		return notFound(err)
	}
	return queries.TouchSupportTicketActivity(ctx, ticketID)
}

// supportSystemNotice is the wording of a system message, in the customer's
// language. The body is stored text rather than a key because the message table
// holds what was said, and a customer reading it next year should read what
// they were told, not what the current catalogue would say.
func supportSystemNotice(locale, event, subject string) string {
	russian := strings.EqualFold(strings.TrimSpace(locale), "ru")
	switch event {
	case "resolved":
		if russian {
			return "Поддержка отметила обращение как решённое. Если вопрос не решён, просто ответьте — обращение откроется заново."
		}
		return "Support marked this request as resolved. If this did not solve it, just reply and the request reopens."
	case "closed":
		if russian {
			return "Поддержка закрыла обращение. Вы можете открыть его заново или создать новое."
		}
		return "Support closed this request. You can reopen it or start a new one."
	case "merged":
		name := strings.TrimSpace(subject)
		if russian {
			if name == "" {
				return "Ваше другое обращение объединено с этим разговором — продолжение здесь."
			}
			return "Ваше обращение «" + name + "» объединено с этим разговором — продолжение здесь."
		}
		if name == "" {
			return "Your other request was merged into this conversation; it continues here."
		}
		return "Your request “" + name + "” was merged into this conversation; it continues here."
	default:
		return ""
	}
}

// MergeTicket folds one ticket into another.
//
// The absorbed ticket keeps its row and points at its survivor, and its
// messages move so the conversation reads as one thread. Deleting it would lose
// the customer's own words and the trail that explains where they went.
func (service *Service) MergeTicket(
	ctx context.Context, ticketID, survivorID string, actor Actor,
) (SupportTicket, error) {
	id, err := parseUUID(ticketID)
	if err != nil {
		return SupportTicket{}, err
	}
	survivor, err := parseUUID(survivorID)
	if err != nil {
		return SupportTicket{}, err
	}
	if ticketID == survivorID {
		return SupportTicket{}, ErrValidaton
	}

	var ticket SupportTicket
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		absorbed, txErr := queries.LockSupportTicket(ctx, id)
		if txErr != nil {
			return notFound(txErr)
		}
		target, txErr := queries.LockSupportTicket(ctx, survivor)
		if txErr != nil {
			return notFound(txErr)
		}
		if absorbed.UserID != target.UserID {
			// Merging across customers would put one customer's words into
			// another's conversation. It is refused rather than warned about.
			return ErrValidaton
		}
		if target.Status == "merged" {
			// A survivor that was itself absorbed would send the customer's
			// words somewhere nobody reads.
			return ErrRejected
		}
		if txErr = queries.MoveSupportMessages(ctx, dbgen.MoveSupportMessagesParams{
			SurvivorID: survivor, TicketID: id,
		}); txErr != nil {
			return txErr
		}
		// The unread counts travel with the messages, so what the customer had
		// not read on the absorbed ticket is still unread on the survivor, and
		// a question an operator had not looked at is still new work.
		if _, txErr = queries.AbsorbSupportTicketCounters(ctx, dbgen.AbsorbSupportTicketCountersParams{
			SurvivorID: survivor, TicketID: id,
		}); txErr != nil {
			return notFound(txErr)
		}
		row, txErr := queries.MergeSupportTicket(ctx, dbgen.MergeSupportTicketParams{
			TicketID: id, SurvivorID: survivor,
		})
		if txErr != nil {
			return notFound(txErr)
		}
		ticket = ticketFrom(row)
		// The customer is told on the surviving thread, which is the one they
		// can still open; the absorbed one points at it.
		if txErr = service.noteToCustomer(ctx, queries, survivor, "merged", absorbed.Subject); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.support_ticket.merged", "support", "support_ticket", ticketID,
			map[string]any{"survivorId": survivorID},
		))
	})
	return ticket, err
}

// ReplyInput is one operator reply.
type ReplyInput struct {
	TicketID         string
	Body             string
	CannedResponseID string
	// DedupeKey makes a resubmitted form reach the message that already exists
	// rather than sending the customer the same answer twice.
	DedupeKey string
}

// Reply appends an operator message and records the first-response time.
//
// The message is written but not sent here. The bot's notifier delivers it,
// which is what keeps consent, quiet hours, and Telegram delivery health in one
// place instead of duplicated into the panel.
func (service *Service) Reply(
	ctx context.Context, input ReplyInput, actor Actor,
) (SupportMessage, error) {
	if strings.TrimSpace(input.Body) == "" {
		return SupportMessage{}, ErrValidaton
	}
	id, err := parseUUID(input.TicketID)
	if err != nil {
		return SupportMessage{}, err
	}
	authorID, err := parseUUID(actor.AdminID)
	if err != nil {
		return SupportMessage{}, ErrValidaton
	}

	var message SupportMessage
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		// The ticket is locked and read first so that "no row" from the insert
		// can only mean the dedupe key: a merged ticket is refused here, with a
		// status that says so, rather than silently producing nothing.
		ticket, txErr := queries.LockSupportTicket(ctx, id)
		if txErr != nil {
			return notFound(txErr)
		}
		if ticket.Status == "merged" {
			return ErrRejected
		}
		row, txErr := queries.AppendOperatorMessage(ctx, dbgen.AppendOperatorMessageParams{
			TicketID: id, Body: strings.TrimSpace(input.Body), AuthorID: authorID,
			CannedResponseID: optionalUUID(input.CannedResponseID),
			DedupeKey:        optionalText(input.DedupeKey),
		})
		if errors.Is(txErr, pgx.ErrNoRows) {
			// The dedupe key already produced this message. A resubmitted form
			// is a no-op, which is the point of the key.
			return nil
		}
		if txErr != nil {
			return txErr
		}
		message = SupportMessage{
			ID: row.ID, Sender: row.Sender, Body: row.Body, Delivery: "queued",
			CreatedAt: timeValue(row.CreatedAt),
		}
		// A reply is activity. Without this a web-only customer's answered
		// ticket never rose in their inbox, because only the bot's delivery
		// mark moved these stamps and it never runs for them.
		if txErr = queries.TouchSupportTicketActivity(ctx, id); txErr != nil {
			return txErr
		}
		// Only the first reply sets the measure, so it survives a conversation
		// that goes back and forth for a week.
		if _, txErr = queries.RecordSupportFirstResponse(ctx, id); txErr != nil &&
			!errors.Is(txErr, pgx.ErrNoRows) {
			return txErr
		}
		if input.CannedResponseID != "" {
			if canned, parseErr := parseUUID(input.CannedResponseID); parseErr == nil {
				if txErr = queries.CountCannedResponseUse(ctx, canned); txErr != nil {
					return txErr
				}
			}
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.support_ticket.replied", "support", "support_ticket", input.TicketID,
			// The body is deliberately absent from the audit metadata. The
			// message itself is the record; copying it into the audit trail
			// would duplicate customer content into a second retention regime.
			map[string]any{"cannedResponseId": input.CannedResponseID},
		))
	})
	return message, err
}

// AddNote records an operator's private note.
func (service *Service) AddNote(
	ctx context.Context, ticketID, body string, actor Actor,
) (SupportNote, error) {
	if strings.TrimSpace(body) == "" {
		return SupportNote{}, ErrValidaton
	}
	id, err := parseUUID(ticketID)
	if err != nil {
		return SupportNote{}, err
	}
	authorID, err := parseUUID(actor.AdminID)
	if err != nil {
		return SupportNote{}, ErrValidaton
	}

	var note SupportNote
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.AppendSupportNote(ctx, dbgen.AppendSupportNoteParams{
			TicketID: id, AuthorID: authorID, Body: strings.TrimSpace(body),
		})
		if txErr != nil {
			return txErr
		}
		note = SupportNote{ID: row.ID, Body: row.Body, CreatedAt: timeValue(row.CreatedAt)}
		return appendAudit(ctx, queries, actor.audit(
			"panel.support_ticket.noted", "support", "support_ticket", ticketID, nil,
		))
	})
	return note, err
}

// SetTicketTag adds or removes a tag.
func (service *Service) SetTicketTag(
	ctx context.Context, ticketID, code string, attach bool, actor Actor,
) error {
	id, err := parseUUID(ticketID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		tag, txErr := queries.GetSupportTagByCode(ctx, code)
		if txErr != nil {
			return notFound(txErr)
		}
		actorID := optionalUUID(actor.AdminID)
		if attach {
			txErr = queries.TagSupportTicket(ctx, dbgen.TagSupportTicketParams{
				TicketID: id, TagID: tag.ID, TaggedBy: actorID,
			})
		} else {
			txErr = queries.UntagSupportTicket(ctx, dbgen.UntagSupportTicketParams{
				TicketID: id, TagID: tag.ID,
			})
		}
		if txErr != nil {
			return txErr
		}
		action := "panel.support_ticket.tagged"
		if !attach {
			action = "panel.support_ticket.untagged"
		}
		return appendAudit(ctx, queries, actor.audit(
			action, "support", "support_ticket", ticketID, map[string]any{"tag": code},
		))
	})
}

// SupportTag is one label the desk uses.
type SupportTag struct {
	ID     string `json:"id"`
	Code   string `json:"code"`
	NameEN string `json:"nameEn"`
	NameRU string `json:"nameRu"`
}

// SupportTags lists the available labels.
func (service *Service) SupportTags(ctx context.Context) ([]SupportTag, error) {
	rows, err := service.queries().ListSupportTags(ctx)
	if err != nil {
		return nil, err
	}
	tags := make([]SupportTag, 0, len(rows))
	for _, row := range rows {
		tags = append(tags, SupportTag{
			ID: uuidString(row.ID), Code: row.Code, NameEN: row.NameEn, NameRU: row.NameRu,
		})
	}
	return tags, nil
}

// SaveSupportTag creates or renames a label.
func (service *Service) SaveSupportTag(
	ctx context.Context, tag SupportTag, actor Actor,
) (SupportTag, error) {
	if strings.TrimSpace(tag.Code) == "" ||
		strings.TrimSpace(tag.NameEN) == "" || strings.TrimSpace(tag.NameRU) == "" {
		return SupportTag{}, ErrValidaton
	}
	var saved SupportTag
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.UpsertSupportTag(ctx, dbgen.UpsertSupportTagParams{
			Code:   strings.ToLower(strings.TrimSpace(tag.Code)),
			NameEn: tag.NameEN, NameRu: tag.NameRU,
		})
		if txErr != nil {
			return txErr
		}
		saved = SupportTag{
			ID: uuidString(row.ID), Code: row.Code, NameEN: row.NameEn, NameRU: row.NameRu,
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.support_tag.saved", "configuration", "support_tag", saved.ID,
			map[string]any{"code": saved.Code},
		))
	})
	return saved, err
}

// CannedResponse is a reusable reply in both languages.
type CannedResponse struct {
	ID                 string `json:"id"`
	Code               string `json:"code"`
	TitleEN            string `json:"titleEn"`
	TitleRU            string `json:"titleRu"`
	BodyEN             string `json:"bodyEn"`
	BodyRU             string `json:"bodyRu"`
	RequiresPermission string `json:"requiresPermission"`
	UsageCount         int64  `json:"usageCount"`
}

// CannedResponses lists the reusable replies, most-used first.
func (service *Service) CannedResponses(ctx context.Context) ([]CannedResponse, error) {
	rows, err := service.queries().ListCannedResponses(ctx)
	if err != nil {
		return nil, err
	}
	responses := make([]CannedResponse, 0, len(rows))
	for _, row := range rows {
		responses = append(responses, cannedFrom(row))
	}
	return responses, nil
}

// SaveCannedResponse creates or updates a reusable reply.
//
// Both languages are required, and the form says so rather than accepting one:
// a canned reply that exists in a single language is one an operator will send
// to the wrong half of the customer base, because the language of the customer
// is not the language of the operator.
func (service *Service) SaveCannedResponse(
	ctx context.Context, response CannedResponse, actor Actor,
) (CannedResponse, error) {
	if strings.TrimSpace(response.Code) == "" ||
		strings.TrimSpace(response.TitleEN) == "" || strings.TrimSpace(response.TitleRU) == "" ||
		strings.TrimSpace(response.BodyEN) == "" || strings.TrimSpace(response.BodyRU) == "" {
		return CannedResponse{}, ErrValidaton
	}
	permission := response.RequiresPermission
	if strings.TrimSpace(permission) == "" {
		permission = "support.write"
	}

	var saved CannedResponse
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.UpsertCannedResponse(ctx, dbgen.UpsertCannedResponseParams{
			Code:    strings.ToLower(strings.TrimSpace(response.Code)),
			TitleEn: response.TitleEN, TitleRu: response.TitleRU,
			BodyEn: response.BodyEN, BodyRu: response.BodyRU,
			RequiresPermission: permission,
			UpdatedBy:          optionalUUID(actor.AdminID),
		})
		if txErr != nil {
			return txErr
		}
		saved = cannedFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.canned_response.saved", "configuration", "canned_response", saved.ID,
			map[string]any{"code": saved.Code, "requiresPermission": permission},
		))
	})
	return saved, err
}

// ArchiveCannedResponse retires a reply without deleting the record of what was
// sent with it.
func (service *Service) ArchiveCannedResponse(
	ctx context.Context, responseID string, actor Actor,
) error {
	id, err := parseUUID(responseID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.ArchiveCannedResponse(ctx, id); txErr != nil {
			return notFound(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.canned_response.archived", "configuration", "canned_response", responseID, nil,
		))
	})
}

// SupportReport is the desk's workload and response-time picture.
//
// The definitions travel with the numbers because a response-time report whose
// definition is ambiguous is a report people argue about instead of acting on.
type SupportReport struct {
	Open       int64 `json:"openTickets"`
	Unassigned int64 `json:"unassignedTickets"`
	Breached   int64 `json:"breachedTickets"`
	Resolved   int64 `json:"resolvedInWindow"`
	// MedianFirstResponseSeconds is a median rather than a mean: one ticket
	// answered a week late would otherwise make a good week look bad.
	MedianFirstResponseSeconds int64          `json:"medianFirstResponseSeconds"`
	WindowSeconds              int64          `json:"windowSeconds"`
	Operators                  []OperatorLoad `json:"operators"`
}

// The definitions that used to travel in this payload now live in the panel's
// message catalogues, beside every other piece of operator-facing copy.
//
// They were a map of hard-coded English prose, which meant a Russian operator
// read the numbers in their own language and the explanation of those numbers
// in somebody else's. The panel renders each definition under the figure it
// defines rather than as a list at the foot of the screen, which is where they
// were and where nobody read them.

// OperatorLoad is one operator's share of the desk.
type OperatorLoad struct {
	OperatorID                 string `json:"operatorId"`
	DisplayName                string `json:"displayName"`
	Replies                    int64  `json:"replies"`
	OpenTickets                int64  `json:"openTickets"`
	ResolvedTickets            int64  `json:"resolvedTickets"`
	MedianFirstResponseSeconds int64  `json:"medianFirstResponseSeconds"`
}

// SupportReport builds the workload report over a window.
func (service *Service) SupportReport(
	ctx context.Context, window time.Duration,
) (SupportReport, error) {
	if window <= 0 {
		window = 7 * 24 * time.Hour
	}
	since := pgtype.Timestamptz{Time: time.Now().UTC().Add(-window), Valid: true}
	queries := service.queries()

	summary, err := queries.SupportDeskSummary(ctx, since)
	if err != nil {
		return SupportReport{}, err
	}
	rows, err := queries.SupportWorkloadReport(ctx, since)
	if err != nil {
		return SupportReport{}, err
	}

	report := SupportReport{
		Open: summary.OpenTickets, Unassigned: summary.UnassignedTickets,
		Breached: summary.BreachedTickets, Resolved: summary.ResolvedInWindow,
		MedianFirstResponseSeconds: summary.MedianFirstResponseSeconds,
		WindowSeconds:              int64(window.Seconds()),
		Operators:                  make([]OperatorLoad, 0, len(rows)),
	}
	for _, row := range rows {
		report.Operators = append(report.Operators, OperatorLoad{
			OperatorID: uuidString(row.OperatorID), DisplayName: row.DisplayName,
			Replies: row.Replies, OpenTickets: row.OpenTickets,
			ResolvedTickets:            row.ResolvedTickets,
			MedianFirstResponseSeconds: row.MedianFirstResponseSeconds,
		})
	}
	return report, nil
}

// MarkTicketRead clears the operator-side unread counter.
func (service *Service) MarkTicketRead(ctx context.Context, ticketID string) error {
	id, err := parseUUID(ticketID)
	if err != nil {
		return err
	}
	_, err = service.queries().MarkSupportTicketRead(ctx, id)
	return notFound(err)
}

var (
	validTicketStatus = map[string]bool{
		"open": true, "pending": true, "resolved": true, "closed": true,
	}
	validTicketPriority = map[string]bool{
		"low": true, "normal": true, "high": true, "urgent": true,
	}
)

func ticketFrom(row dbgen.SupportTicket) SupportTicket {
	ticket := SupportTicket{
		ID: uuidString(row.ID), CustomerID: uuidString(row.UserID),
		QueueID: uuidString(row.QueueID), Subject: row.Subject,
		Status: row.Status, Priority: row.Priority,
		AssigneeID:  uuidString(row.AssigneeID),
		Tags:        []string{},
		Unread:      row.OperatorUnreadCount,
		Reopened:    row.ReopenedCount,
		MergedInto:  uuidString(row.MergedIntoTicketID),
		CreatedAt:   timeValue(row.CreatedAt),
		LastMessage: timeValue(row.LastMessageAt),
	}
	if row.FirstResponseAt.Valid {
		first := timeValue(row.FirstResponseAt)
		ticket.FirstReply = &first
	}
	if row.ResolvedAt.Valid {
		resolved := timeValue(row.ResolvedAt)
		ticket.ResolvedAt = &resolved
	}
	return ticket
}

func cannedFrom(row dbgen.SupportCannedResponse) CannedResponse {
	return CannedResponse{
		ID: uuidString(row.ID), Code: row.Code,
		TitleEN: row.TitleEn, TitleRU: row.TitleRu,
		BodyEN: row.BodyEn, BodyRU: row.BodyRu,
		RequiresPermission: row.RequiresPermission,
		UsageCount:         row.UsageCount,
	}
}
