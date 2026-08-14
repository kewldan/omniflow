package httpapi

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/omniflow/omniflow/internal/accountsupport"
)

// mountSupport registers the support, news, and communication-preference routes
// on the authenticated customer router.
//
// A nil service leaves the whole surface absent rather than present and failing.
// The route table then describes what this installation can actually do, which
// is what a panel probing for its own capabilities needs to read.
func (handlers *AccountHandlers) mountSupport(router chi.Router) {
	if handlers.support == nil {
		return
	}

	// The limits are published before an upload rather than only enforced after
	// one. A customer who learns the rule from a refusal has already spent the
	// time it took to send the file.
	router.Get("/support/limits", handlers.supportLimits)

	router.Get("/support/tickets", handlers.listSupportTickets)
	router.Post("/support/tickets", handlers.createSupportTicket)
	router.Get("/support/tickets/{ticketID}", handlers.supportConversation)
	router.Post("/support/tickets/{ticketID}/messages", handlers.replySupportTicket)
	router.Post("/support/tickets/{ticketID}/read", handlers.readSupportTicket)
	router.Post("/support/tickets/{ticketID}/close", handlers.closeSupportTicket)
	router.Post("/support/tickets/{ticketID}/reopen", handlers.reopenSupportTicket)
	router.Post("/support/tickets/{ticketID}/attachments", handlers.uploadSupportAttachment)
	router.Get("/support/attachments/{attachmentID}", handlers.downloadSupportAttachment)

	router.Get("/news", handlers.listSupportNews)
	router.Post("/news/{postID}/read", handlers.readSupportNews)

	// What was actually delivered, beside the settings that decided it. The
	// preferences above say what should arrive; this says what did, and why
	// anything did not.
	router.Get("/notifications", handlers.notificationHistory)

	router.Get("/preferences", handlers.communicationPreferences)
	router.Patch("/preferences", handlers.updateCommunicationPreferences)
	router.Post("/preferences/unsubscribe", handlers.unsubscribeFromMarketing)
}

// ---------------------------------------------------------------------------
// Support tickets
// ---------------------------------------------------------------------------

func (handlers *AccountHandlers) supportLimits(writer http.ResponseWriter, request *http.Request) {
	limits := handlers.support.Limits()
	writeJSON(writer, http.StatusOK, map[string]any{
		"maxAttachmentBytes": limits.MaxAttachmentBytes,
		"allowedMediaTypes":  limits.AllowedMediaTypes,
		"maxOpenTickets":     limits.MaxOpenTickets,
		"maxMessageLength":   accountsupport.MaxMessageLength,
		"maxSubjectLength":   accountsupport.MaxSubjectLength,
	})
}

func (handlers *AccountHandlers) listSupportTickets(
	writer http.ResponseWriter, request *http.Request,
) {
	principal, _ := CustomerFrom(request.Context())
	page, err := handlers.support.Tickets(
		request.Context(), principal.Customer.ID,
		trimmedQuery(request, "cursor"), queryLimit(request),
	)
	if handlers.writeSupportError(writer, request, err) {
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, ticket := range page.Items {
		items = append(items, supportTicketPayload(ticket))
	}
	payload := map[string]any{"items": items}
	if page.NextCursor != "" {
		payload["nextCursor"] = page.NextCursor
	}
	writeJSON(writer, http.StatusOK, payload)
}

// supportTicketPayload is the wire shape of one conversation.
//
// `open` and `canReply` are both computed here rather than left to the panel.
// A resolved ticket still accepts a reply and no longer counts against the open
// quota, and a client deriving either rule from the status string would be a
// second place the rule lives.
func supportTicketPayload(ticket accountsupport.Ticket) map[string]any {
	payload := map[string]any{
		"id": ticket.ID, "subject": ticket.Subject, "status": ticket.Status,
		"priority": ticket.Priority, "open": ticket.Open, "canReply": ticket.CanReply,
		"unreadCount": ticket.Unread, "messageCount": ticket.MessageCount,
		"createdAt":     ticket.CreatedAt.Format(time.RFC3339),
		"updatedAt":     ticket.UpdatedAt.Format(time.RFC3339),
		"lastMessageAt": ticket.LastMessageAt.Format(time.RFC3339),
	}
	if ticket.MergedInto != "" {
		payload["mergedIntoTicketId"] = ticket.MergedInto
	}
	return payload
}

func supportConversationPayload(conversation accountsupport.Conversation) map[string]any {
	messages := make([]map[string]any, 0, len(conversation.Messages))
	for _, message := range conversation.Messages {
		attachments := make([]map[string]any, 0, len(message.Attachments))
		for _, attachment := range message.Attachments {
			attachments = append(attachments, map[string]any{
				"id": attachment.ID, "kind": attachment.Kind,
				"fileName": attachment.FileName, "mediaType": attachment.MediaType,
				"sizeBytes": attachment.SizeBytes, "downloadable": attachment.Downloadable,
				"createdAt": attachment.CreatedAt.Format(time.RFC3339),
			})
		}
		messages = append(messages, map[string]any{
			"id": message.ID, "author": message.Author, "body": message.Body,
			"unread":      message.Unread,
			"createdAt":   message.CreatedAt.Format(time.RFC3339),
			"attachments": attachments,
		})
	}
	return map[string]any{
		"ticket": supportTicketPayload(conversation.Ticket), "messages": messages,
	}
}

func (handlers *AccountHandlers) createSupportTicket(
	writer http.ResponseWriter, request *http.Request,
) {
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		Subject string `json:"subject"`
		Message string `json:"message"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	// The header is optional here, unlike on an operator reply. A customer whose
	// browser resubmits a form should not be shown a validation error about a
	// header they have never heard of; supplying one buys deduplication, and
	// omitting one costs only that.
	conversation, err := handlers.support.CreateTicket(request.Context(), accountsupport.NewTicket{
		CustomerID: principal.Customer.ID, Subject: body.Subject, Body: body.Message,
		IdempotencyKey: idempotencyKey(request),
	})
	if handlers.writeSupportError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusCreated, supportConversationPayload(conversation))
}

func (handlers *AccountHandlers) supportConversation(
	writer http.ResponseWriter, request *http.Request,
) {
	principal, _ := CustomerFrom(request.Context())
	conversation, err := handlers.support.Conversation(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "ticketID"),
	)
	if handlers.writeSupportError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, supportConversationPayload(conversation))
}

func (handlers *AccountHandlers) replySupportTicket(
	writer http.ResponseWriter, request *http.Request,
) {
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		Message string `json:"message"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	conversation, err := handlers.support.Reply(request.Context(), accountsupport.NewMessage{
		CustomerID: principal.Customer.ID, TicketID: chi.URLParam(request, "ticketID"),
		Body: body.Message, IdempotencyKey: idempotencyKey(request),
	})
	if handlers.writeSupportError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, supportConversationPayload(conversation))
}

func (handlers *AccountHandlers) readSupportTicket(
	writer http.ResponseWriter, request *http.Request,
) {
	principal, _ := CustomerFrom(request.Context())
	ticket, err := handlers.support.MarkRead(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "ticketID"),
	)
	if handlers.writeSupportError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, supportTicketPayload(ticket))
}

func (handlers *AccountHandlers) closeSupportTicket(
	writer http.ResponseWriter, request *http.Request,
) {
	principal, _ := CustomerFrom(request.Context())
	ticket, err := handlers.support.Close(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "ticketID"),
	)
	if handlers.writeSupportError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, supportTicketPayload(ticket))
}

func (handlers *AccountHandlers) reopenSupportTicket(
	writer http.ResponseWriter, request *http.Request,
) {
	principal, _ := CustomerFrom(request.Context())
	ticket, err := handlers.support.Reopen(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "ticketID"),
	)
	if handlers.writeSupportError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, supportTicketPayload(ticket))
}

// ---------------------------------------------------------------------------
// Attachments
// ---------------------------------------------------------------------------

// uploadSupportAttachment accepts one file as a new message on the conversation.
//
// The body is bounded before it is parsed rather than after. Reading an
// unbounded multipart body to discover it was too large is how a size limit
// becomes a way to exhaust memory, so the reader is capped at the configured
// limit plus a small allowance for the multipart framing itself.
func (handlers *AccountHandlers) uploadSupportAttachment(
	writer http.ResponseWriter, request *http.Request,
) {
	principal, _ := CustomerFrom(request.Context())
	limits := handlers.support.Limits()
	const multipartOverhead = 64 << 10
	request.Body = http.MaxBytesReader(writer, request.Body, limits.MaxAttachmentBytes+multipartOverhead)
	if err := request.ParseMultipartForm(multipartOverhead); err != nil {
		// The two failures are told apart because the remedies are different: a
		// file over the cap can be made smaller, and a body the parser could not
		// read is a client fault the customer cannot act on at all.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(
				writer, request, http.StatusRequestEntityTooLarge,
				"attachment_too_large", "That file is larger than this installation accepts",
			)
			return
		}
		writeProblem(writer, request, http.StatusBadRequest, "upload_failed", "The upload could not be read")
		return
	}
	defer func() { _ = request.MultipartForm.RemoveAll() }()

	file, header, err := request.FormFile("file")
	if err != nil {
		writeProblem(writer, request, http.StatusUnprocessableEntity, "file_required", "A file is required")
		return
	}
	defer func() { _ = file.Close() }()
	if header.Size > limits.MaxAttachmentBytes {
		writeProblem(
			writer, request, http.StatusRequestEntityTooLarge,
			"attachment_too_large", "That file is larger than this installation accepts",
		)
		return
	}
	// The buffer is sized from the part header and filled exactly. A short read
	// must not be stored: the digest the file is named by would be of bytes the
	// customer never sent.
	content := make([]byte, header.Size)
	if _, err = io.ReadFull(file, content); err != nil {
		writeProblem(writer, request, http.StatusBadRequest, "upload_failed", "The upload did not complete")
		return
	}
	attachment, err := handlers.support.Attach(request.Context(), accountsupport.NewAttachment{
		CustomerID: principal.Customer.ID, TicketID: chi.URLParam(request, "ticketID"),
		Body:      request.FormValue("message"),
		FileName:  header.Filename,
		MediaType: header.Header.Get("Content-Type"),
		Content:   content,
	})
	if handlers.writeSupportError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{
		"id": attachment.ID, "messageId": attachment.MessageID, "kind": attachment.Kind,
		"fileName": attachment.FileName, "mediaType": attachment.MediaType,
		"sizeBytes": attachment.SizeBytes, "downloadable": attachment.Downloadable,
		"createdAt": attachment.CreatedAt.Format(time.RFC3339),
	})
}

// downloadSupportAttachment serves one file to the customer who owns it.
//
// Three things make the response safe to hand to a browser. The content type is
// the stored one only while it is still on the allowlist, so shrinking the
// allowlist retroactively neutralises what was accepted under a wider one;
// `nosniff` stops the browser from looking for a more interesting type than the
// one declared; and `Content-Disposition: attachment` means even a type that
// could render never renders in this origin.
func (handlers *AccountHandlers) downloadSupportAttachment(
	writer http.ResponseWriter, request *http.Request,
) {
	principal, _ := CustomerFrom(request.Context())
	attachment, content, err := handlers.support.Attachment(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "attachmentID"),
	)
	if handlers.writeSupportError(writer, request, err) {
		return
	}
	mediaType := "application/octet-stream"
	if handlers.support.Limits().MediaTypeAllowed(attachment.MediaType) {
		mediaType = attachment.MediaType
	}
	fileName := attachment.FileName
	if strings.TrimSpace(fileName) == "" {
		fileName = "attachment"
	}
	writer.Header().Set("Content-Type", mediaType)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
	// FormatMediaType quotes and encodes the name, so a file called `"; x=` is a
	// file name rather than a way to add a header parameter.
	writer.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": fileName}))
	writer.WriteHeader(http.StatusOK)
	if _, err = writer.Write(content); err != nil {
		handlers.logger.Warn("support attachment download was interrupted", "error", err)
	}
}

// ---------------------------------------------------------------------------
// News
// ---------------------------------------------------------------------------

func (handlers *AccountHandlers) listSupportNews(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	page, err := handlers.support.News(
		request.Context(), principal.Customer.ID, trimmedQuery(request, "locale"),
		trimmedQuery(request, "cursor"), queryLimit(request),
	)
	if handlers.writeSupportError(writer, request, err) {
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, map[string]any{
			"id": item.ID, "slug": item.Slug, "category": item.Category,
			"class": item.Class, "title": item.Title, "body": item.Body,
			"read":        item.Read,
			"publishedAt": item.PublishedAt.Format(time.RFC3339),
		})
	}
	payload := map[string]any{"items": items, "unreadCount": page.Unread, "locale": page.Locale}
	if page.NextCursor != "" {
		payload["nextCursor"] = page.NextCursor
	}
	writeJSON(writer, http.StatusOK, payload)
}

func (handlers *AccountHandlers) readSupportNews(writer http.ResponseWriter, request *http.Request) {
	principal, _ := CustomerFrom(request.Context())
	err := handlers.support.MarkNewsRead(
		request.Context(), principal.Customer.ID, chi.URLParam(request, "postID"),
	)
	if handlers.writeSupportError(writer, request, err) {
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Notification history
// ---------------------------------------------------------------------------

// notificationHistory answers "did you send it, and when".
//
// The customer identifier comes from the session and from nowhere else — there
// is no path parameter to point at somebody else's history, because there is no
// reason a customer would ever read one.
func (handlers *AccountHandlers) notificationHistory(
	writer http.ResponseWriter, request *http.Request,
) {
	principal, _ := CustomerFrom(request.Context())
	deliveries, err := handlers.support.Deliveries(
		request.Context(), principal.Customer.ID, queryLimit(request),
	)
	if handlers.writeSupportError(writer, request, err) {
		return
	}
	items := make([]map[string]any, 0, len(deliveries))
	for _, delivery := range deliveries {
		item := map[string]any{
			"kind": delivery.Kind, "status": delivery.Status,
			"scheduledAt": delivery.ScheduledAt.Format(time.RFC3339),
		}
		if delivery.Reason != "" {
			item["reason"] = delivery.Reason
		}
		if !delivery.SentAt.IsZero() {
			item["sentAt"] = delivery.SentAt.Format(time.RFC3339)
		}
		if !delivery.DeferredUntil.IsZero() {
			item["deferredUntil"] = delivery.DeferredUntil.Format(time.RFC3339)
		}
		if delivery.SubscriptionSlot > 0 {
			item["subscriptionSlot"] = delivery.SubscriptionSlot
			if delivery.SubscriptionLabel != "" {
				item["subscriptionLabel"] = delivery.SubscriptionLabel
			}
		}
		items = append(items, item)
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

// ---------------------------------------------------------------------------
// Communication preferences
// ---------------------------------------------------------------------------

func supportPreferencesPayload(preferences accountsupport.Preferences) map[string]any {
	contacts := make([]map[string]any, 0, len(preferences.Contacts))
	for _, contact := range preferences.Contacts {
		// The address is deliberately absent. The panel says "your email" and the
		// customer already knows which one; sending the value back would put a
		// contact detail into every response that renders this screen.
		contacts = append(contacts, map[string]any{
			"id": contact.ID, "kind": contact.Kind, "verified": contact.Verified,
			"transactional": contact.Transactional, "marketing": contact.Marketing,
			"createdAt": contact.CreatedAt.Format(time.RFC3339),
		})
	}
	marketing := map[string]any{"enabled": preferences.Marketing.Enabled}
	if !preferences.Marketing.DecidedAt.IsZero() {
		marketing["decidedAt"] = preferences.Marketing.DecidedAt.Format(time.RFC3339)
		marketing["source"] = preferences.Marketing.Source
		marketing["policyVersion"] = preferences.Marketing.PolicyVersion
	}
	payload := map[string]any{
		"locale": preferences.Locale,
		"notifications": map[string]any{
			"expiry":  preferences.Notifications.Expiry,
			"traffic": preferences.Notifications.Traffic,
			"renewal": preferences.Notifications.Renewal,
			"news":    preferences.Notifications.News,
		},
		"marketing": marketing,
		"contacts":  contacts,
	}
	if window := preferences.QuietHours; window != nil {
		payload["quietHours"] = map[string]any{
			"startHour": window.StartHour, "endHour": window.EndHour,
		}
	}
	if suppression := preferences.Suppression; suppression != nil {
		payload["suppression"] = map[string]any{
			"reason":    suppression.Reason,
			"createdAt": suppression.CreatedAt.Format(time.RFC3339),
		}
	}
	return payload
}

func (handlers *AccountHandlers) communicationPreferences(
	writer http.ResponseWriter, request *http.Request,
) {
	principal, _ := CustomerFrom(request.Context())
	preferences, err := handlers.support.Preferences(request.Context(), principal.Customer.ID)
	if handlers.writeSupportError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, supportPreferencesPayload(preferences))
}

// updateCommunicationPreferences applies a partial change.
//
// Every field is a pointer, which is what makes this a PATCH rather than a PUT
// wearing the wrong verb: an absent field is left alone, so a panel that renders
// one switch cannot silently rewrite the others it never showed.
func (handlers *AccountHandlers) updateCommunicationPreferences(
	writer http.ResponseWriter, request *http.Request,
) {
	principal, _ := CustomerFrom(request.Context())
	var body struct {
		Locale        *string `json:"locale"`
		Notifications *struct {
			Expiry  *bool `json:"expiry"`
			Traffic *bool `json:"traffic"`
			Renewal *bool `json:"renewal"`
			News    *bool `json:"news"`
		} `json:"notifications"`
		Marketing  *bool `json:"marketing"`
		QuietHours *struct {
			StartHour int `json:"startHour"`
			EndHour   int `json:"endHour"`
		} `json:"quietHours"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	update := accountsupport.PreferencesUpdate{Locale: body.Locale, Marketing: body.Marketing}
	if body.Notifications != nil {
		update.Expiry = body.Notifications.Expiry
		update.Traffic = body.Notifications.Traffic
		update.Renewal = body.Notifications.Renewal
		update.News = body.Notifications.News
	}
	if body.QuietHours != nil {
		update.QuietHours = &accountsupport.QuietWindow{
			StartHour: body.QuietHours.StartHour, EndHour: body.QuietHours.EndHour,
		}
	}
	preferences, err := handlers.support.UpdatePreferences(
		request.Context(), principal.Customer.ID, update, handlers.consentContext(request),
	)
	if handlers.writeSupportError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, supportPreferencesPayload(preferences))
}

// unsubscribeFromMarketing is the one-action opt-out.
//
// It takes no body. An unsubscribe control that needs a form to be filled in
// correctly is one that can fail for somebody who has already decided, and the
// only thing this route needs to know is who is asking.
func (handlers *AccountHandlers) unsubscribeFromMarketing(
	writer http.ResponseWriter, request *http.Request,
) {
	principal, _ := CustomerFrom(request.Context())
	preferences, err := handlers.support.Unsubscribe(
		request.Context(), principal.Customer.ID, handlers.consentContext(request),
	)
	if handlers.writeSupportError(writer, request, err) {
		return
	}
	writeJSON(writer, http.StatusOK, supportPreferencesPayload(preferences))
}

// consentContext names the surface and the request that recorded a choice, so a
// consent row can be traced back to the request that wrote it.
func (handlers *AccountHandlers) consentContext(request *http.Request) accountsupport.RequestContext {
	return accountsupport.RequestContext{
		Surface: "customer_web", RequestID: middleware.GetReqID(request.Context()),
	}
}

// ---------------------------------------------------------------------------
// Shared plumbing
// ---------------------------------------------------------------------------

func idempotencyKey(request *http.Request) string {
	return strings.TrimSpace(request.Header.Get("Idempotency-Key"))
}

func queryLimit(request *http.Request) int {
	limit, err := strconv.Atoi(trimmedQuery(request, "limit"))
	if err != nil {
		return 0
	}
	return limit
}

// writeSupportError maps this surface's errors onto responses, reporting whether
// it wrote one.
//
// It handles its own sentinels and hands everything else to the shared account
// writer, which already knows how to answer an invalid input. Nothing here
// carries a message body, a file name, or a contact value into a problem detail:
// a problem document is logged and forwarded by things this package does not
// control.
func (handlers *AccountHandlers) writeSupportError(
	writer http.ResponseWriter, request *http.Request, err error,
) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, accountsupport.ErrNotFound):
		// "Not yours" and "does not exist" answer identically, so an identifier
		// cannot be probed for existence by watching the status code.
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That was not found")
	case errors.Is(err, accountsupport.ErrTicketClosed):
		writeProblem(
			writer, request, http.StatusConflict,
			"ticket_closed", "This conversation is closed. Reopen it to reply",
		)
	case errors.Is(err, accountsupport.ErrTooManyOpenTickets):
		writeProblem(
			writer, request, http.StatusConflict, "too_many_open_tickets",
			fmt.Sprintf(
				"You already have %d open conversations. Continue one of them instead",
				handlers.support.Limits().MaxOpenTickets,
			),
		)
	case errors.Is(err, accountsupport.ErrAttachmentTooLarge):
		writeProblem(
			writer, request, http.StatusRequestEntityTooLarge,
			"attachment_too_large", "That file is larger than this installation accepts",
		)
	case errors.Is(err, accountsupport.ErrAttachmentMediaType):
		writeProblem(
			writer, request, http.StatusUnsupportedMediaType,
			"attachment_media_type", "That kind of file is not accepted",
		)
	case errors.Is(err, accountsupport.ErrAttachmentRemote):
		writeProblem(
			writer, request, http.StatusConflict, "attachment_remote",
			"This file was sent through Telegram and can only be opened there",
		)
	case errors.Is(err, accountsupport.ErrAttachmentStorage):
		handlers.logger.Error("support attachment storage failed", "error", err)
		writeProblem(
			writer, request, http.StatusServiceUnavailable,
			"attachment_storage_unavailable", "Files cannot be stored right now",
		)
	case errors.Is(err, accountsupport.ErrQueueMissing):
		handlers.logger.Error("no default support queue is configured")
		writeProblem(
			writer, request, http.StatusServiceUnavailable,
			"support_unavailable", "Support is not available right now",
		)
	default:
		return handlers.writeAccountError(writer, request, err)
	}
	return true
}
