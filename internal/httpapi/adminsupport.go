package httpapi

import (
	"errors"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/accountsupport"
	"github.com/omniflow/omniflow/internal/panelpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// mountSupport registers the support desk.
//
// Reading a ticket and answering one are separate permissions, because they are
// separate jobs: a finance operator investigating a refund needs to read the
// conversation that led to it, and should not be able to reply to the customer
// in the desk's voice.
func (handlers *AdminHandlers) mountSupport(secure chi.Router) {
	if handlers.operations == nil {
		return
	}

	secure.With(handlers.requirePermission(rbac.PermissionSupportRead)).Group(func(read chi.Router) {
		read.Get("/support/queues", handlers.supportQueues)
		read.Get("/support/tickets", handlers.searchTickets)
		read.Get("/support/tickets/{ticketID}", handlers.supportTicket)
		read.Get("/support/tickets/{ticketID}/attachments/{attachmentID}", handlers.downloadTicketAttachment)
		read.Get("/support/tags", handlers.supportTags)
		read.Get("/support/canned", handlers.cannedResponses)
		read.Get("/support/report", handlers.supportReport)
	})

	secure.With(handlers.requirePermission(rbac.PermissionSupportWrite)).Group(func(write chi.Router) {
		write.Post("/support/tickets/{ticketID}/assign", handlers.assignTicket)
		write.Post("/support/tickets/{ticketID}/queue", handlers.moveTicket)
		write.Post("/support/tickets/{ticketID}/priority", handlers.setTicketPriority)
		write.Post("/support/tickets/{ticketID}/status", handlers.setTicketStatus)
		write.Post("/support/tickets/{ticketID}/merge", handlers.mergeTicket)
		write.Post("/support/tickets/{ticketID}/reply", handlers.replyToTicket)
		write.Post("/support/tickets/{ticketID}/notes", handlers.addTicketNote)
		write.Post("/support/tickets/{ticketID}/read", handlers.markTicketRead)
		write.Put("/support/tickets/{ticketID}/tags/{tag}", handlers.attachTag)
		write.Delete("/support/tickets/{ticketID}/tags/{tag}", handlers.detachTag)
	})

	// Queues, tags, and canned responses are configuration rather than
	// day-to-day support work, so they sit behind the settings permission. An
	// operator answering tickets should not be able to redefine the promises
	// their queue makes.
	secure.With(handlers.requirePermission(rbac.PermissionSettingsWrite)).Group(func(write chi.Router) {
		write.Put("/support/queues", handlers.saveSupportQueue)
		write.Put("/support/tags", handlers.saveSupportTag)
		write.Put("/support/canned", handlers.saveCannedResponse)
		write.Delete("/support/canned/{responseID}", handlers.archiveCannedResponse)
	})
}

func (handlers *AdminHandlers) supportQueues(writer http.ResponseWriter, request *http.Request) {
	queues, err := handlers.operations.SupportQueues(request.Context())
	handlers.respond(writer, request, map[string]any{"items": queues}, err)
}

func (handlers *AdminHandlers) searchTickets(writer http.ResponseWriter, request *http.Request) {
	page, err := handlers.operations.SearchTickets(request.Context(), panelpg.TicketFilter{
		QueueID:        query(request, "queueId"),
		Status:         query(request, "status"),
		Priority:       query(request, "priority"),
		AssigneeID:     query(request, "assigneeId"),
		UnassignedOnly: query(request, "unassigned") == "true",
		CustomerID:     query(request, "customerId"),
		Tag:            query(request, "tag"),
		Cursor:         query(request, "cursor"),
		PageSize:       int32(queryInt(request, "pageSize")),
	})
	handlers.respond(writer, request, page, err)
}

func (handlers *AdminHandlers) supportTicket(writer http.ResponseWriter, request *http.Request) {
	detail, err := handlers.operations.Ticket(request.Context(), chi.URLParam(request, "ticketID"))
	handlers.respond(writer, request, detail, err)
}

// downloadTicketAttachment serves a file a customer uploaded through the web
// panel to an operator reading the ticket.
//
// It needs only `support.read`: the file is part of the conversation, and an
// operator entitled to read the words is entitled to read the screenshot that
// came with them. A file that arrived through Telegram is answered with 409
// `attachment_remote` and never fetched — Omniflow holds a reference, not the
// bytes, and proxying Telegram's file API with the bot token on an operator's
// behalf would put a customer's file behind a credential the panel was never
// meant to hold. The response headers match the customer download exactly:
// `attachment` disposition, `nosniff`, and the stored type only while it is on
// the allowlist.
func (handlers *AdminHandlers) downloadTicketAttachment(writer http.ResponseWriter, request *http.Request) {
	attachment, key, err := handlers.operations.TicketAttachment(
		request.Context(), chi.URLParam(request, "ticketID"), chi.URLParam(request, "attachmentID"),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	if !attachment.Downloadable {
		writeProblem(
			writer, request, http.StatusConflict, "attachment_remote",
			"This file was sent through Telegram and lives there; it cannot be downloaded from the panel",
		)
		return
	}
	if handlers.supportFiles == nil {
		writeProblem(
			writer, request, http.StatusServiceUnavailable, "attachment_storage_unavailable",
			"Attachment storage is not configured for this process",
		)
		return
	}
	content, err := handlers.supportFiles.Open(request.Context(), key)
	if errors.Is(err, os.ErrNotExist) {
		// The row outlived its file: retention or an operator removed the
		// bytes. That reads as a file that is no longer there.
		writeProblem(writer, request, http.StatusNotFound, "not_found", "That file is no longer stored")
		return
	}
	if err != nil {
		handlers.logger.Error("support attachment could not be read", "error", err)
		writeProblem(
			writer, request, http.StatusServiceUnavailable, "attachment_storage_unavailable",
			"Files cannot be read right now",
		)
		return
	}
	mediaType := "application/octet-stream"
	if accountsupport.DefaultLimits().MediaTypeAllowed(attachment.MediaType) {
		mediaType = attachment.MediaType
	}
	fileName := strings.TrimSpace(attachment.FileName)
	if fileName == "" {
		fileName = "attachment"
	}
	writer.Header().Set("Content-Type", mediaType)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
	writer.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": fileName}))
	writer.WriteHeader(http.StatusOK)
	if _, err = writer.Write(content); err != nil {
		handlers.logger.Warn("support attachment download was interrupted", "error", err)
	}
}

func (handlers *AdminHandlers) assignTicket(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		AssigneeID string `json:"assigneeId"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	ticket, err := handlers.operations.AssignTicket(
		request.Context(), chi.URLParam(request, "ticketID"), body.AssigneeID, actorFrom(request),
	)
	handlers.respond(writer, request, ticket, err)
}

func (handlers *AdminHandlers) moveTicket(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		QueueID string `json:"queueId"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	ticket, err := handlers.operations.MoveTicket(
		request.Context(), chi.URLParam(request, "ticketID"), body.QueueID, actorFrom(request),
	)
	handlers.respond(writer, request, ticket, err)
}

func (handlers *AdminHandlers) setTicketPriority(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Priority string `json:"priority"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	ticket, err := handlers.operations.SetTicketPriority(
		request.Context(), chi.URLParam(request, "ticketID"), body.Priority, actorFrom(request),
	)
	handlers.respond(writer, request, ticket, err)
}

func (handlers *AdminHandlers) setTicketStatus(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Status string `json:"status"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	ticket, err := handlers.operations.SetTicketStatus(
		request.Context(), chi.URLParam(request, "ticketID"), body.Status, actorFrom(request),
	)
	handlers.respond(writer, request, ticket, err)
}

// mergeTicket folds this ticket into another.
//
// The merge is refused across customers by the service, because putting one
// customer's words into another's conversation is not a mistake worth warning
// about afterwards.
func (handlers *AdminHandlers) mergeTicket(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		SurvivorID string `json:"survivorId"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	ticket, err := handlers.operations.MergeTicket(
		request.Context(), chi.URLParam(request, "ticketID"), body.SurvivorID, actorFrom(request),
	)
	handlers.respond(writer, request, ticket, err)
}

// replyToTicket writes an operator message.
//
// The idempotency header becomes the message's dedupe key, so a resubmitted
// form reaches the message that already exists rather than sending the customer
// the same answer twice.
func (handlers *AdminHandlers) replyToTicket(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Body             string `json:"body"`
		CannedResponseID string `json:"cannedResponseId"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if key == "" {
		writeProblem(
			writer, request, http.StatusUnprocessableEntity,
			"idempotency_key_required", "A support reply requires an Idempotency-Key header",
		)
		return
	}
	message, err := handlers.operations.Reply(request.Context(), panelpg.ReplyInput{
		TicketID: chi.URLParam(request, "ticketID"), Body: body.Body,
		CannedResponseID: body.CannedResponseID, DedupeKey: key,
	}, actorFrom(request))
	handlers.respond(writer, request, message, err)
}

func (handlers *AdminHandlers) addTicketNote(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Body string `json:"body"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	note, err := handlers.operations.AddNote(
		request.Context(), chi.URLParam(request, "ticketID"), body.Body, actorFrom(request),
	)
	handlers.respond(writer, request, note, err)
}

func (handlers *AdminHandlers) markTicketRead(writer http.ResponseWriter, request *http.Request) {
	err := handlers.operations.MarkTicketRead(request.Context(), chi.URLParam(request, "ticketID"))
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) attachTag(writer http.ResponseWriter, request *http.Request) {
	handlers.setTag(writer, request, true)
}

func (handlers *AdminHandlers) detachTag(writer http.ResponseWriter, request *http.Request) {
	handlers.setTag(writer, request, false)
}

func (handlers *AdminHandlers) setTag(
	writer http.ResponseWriter, request *http.Request, attach bool,
) {
	err := handlers.operations.SetTicketTag(
		request.Context(), chi.URLParam(request, "ticketID"), chi.URLParam(request, "tag"),
		attach, actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handlers *AdminHandlers) supportTags(writer http.ResponseWriter, request *http.Request) {
	tags, err := handlers.operations.SupportTags(request.Context())
	handlers.respond(writer, request, map[string]any{"items": tags}, err)
}

func (handlers *AdminHandlers) saveSupportTag(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.SupportTag
	if !decodeJSON(writer, request, &body) {
		return
	}
	tag, err := handlers.operations.SaveSupportTag(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, tag, err)
}

func (handlers *AdminHandlers) saveSupportQueue(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.SupportQueue
	if !decodeJSON(writer, request, &body) {
		return
	}
	queue, err := handlers.operations.SaveSupportQueue(request.Context(), body, actorFrom(request))
	handlers.respond(writer, request, queue, err)
}

func (handlers *AdminHandlers) cannedResponses(writer http.ResponseWriter, request *http.Request) {
	responses, err := handlers.operations.CannedResponses(request.Context())
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	// A response the operator may not use is filtered out here rather than
	// shown disabled. A list of replies somebody cannot send is a list they
	// have to read past every time.
	permitted := make([]panelpg.CannedResponse, 0, len(responses))
	for _, response := range responses {
		if principalAllows(request, response.RequiresPermission) {
			permitted = append(permitted, response)
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": permitted})
}

func (handlers *AdminHandlers) saveCannedResponse(writer http.ResponseWriter, request *http.Request) {
	var body panelpg.CannedResponse
	if !decodeJSON(writer, request, &body) {
		return
	}
	response, err := handlers.operations.SaveCannedResponse(
		request.Context(), body, actorFrom(request),
	)
	handlers.respond(writer, request, response, err)
}

func (handlers *AdminHandlers) archiveCannedResponse(
	writer http.ResponseWriter, request *http.Request,
) {
	err := handlers.operations.ArchiveCannedResponse(
		request.Context(), chi.URLParam(request, "responseID"), actorFrom(request),
	)
	if err != nil {
		handlers.operationsError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// supportReport serves the workload and response-time picture.
//
// The window is capped so one request cannot ask the database to compute a
// median across every ticket ever filed.
func (handlers *AdminHandlers) supportReport(writer http.ResponseWriter, request *http.Request) {
	window := time.Duration(queryInt(request, "windowDays")) * 24 * time.Hour
	if window <= 0 {
		window = 7 * 24 * time.Hour
	}
	if window > 90*24*time.Hour {
		window = 90 * 24 * time.Hour
	}
	report, err := handlers.operations.SupportReport(request.Context(), window)
	handlers.respond(writer, request, report, err)
}

// principalAllows reports whether the signed-in operator holds a permission.
//
// It reads the grant the session middleware already resolved rather than
// re-deriving it, so the answer here and the answer the route gate gives can
// never disagree.
func principalAllows(request *http.Request, permission string) bool {
	principal, ok := PrincipalFrom(request.Context())
	if !ok {
		return false
	}
	return principal.Grant.AllowsAll(rbac.Permission(permission))
}

// mountLoyalty registers referral review and loyalty.
//
// Referral review sits behind the risk permission rather than a marketing one:
// deciding that somebody gamed a referral programme and taking their reward
// back is an adverse decision about a customer, which is what `risk.write`
// governs everywhere else in the product.
func (handlers *AdminHandlers) mountLoyalty(secure chi.Router) {
	if handlers.operations == nil {
		return
	}

	secure.With(handlers.requirePermission(rbac.PermissionRiskRead)).
		Get("/referrals/review", handlers.referralReviews)
	secure.With(handlers.requirePermission(rbac.PermissionRiskWrite)).
		Post("/referrals/review/{customerID}", handlers.reviewReferral)

	secure.With(handlers.requirePermission(rbac.PermissionMarketingRead)).Group(func(read chi.Router) {
		read.Get("/loyalty/programs", handlers.loyaltyPrograms)
		read.Get("/customers/{customerID}/loyalty", handlers.customerLoyalty)
	})
	secure.With(handlers.requirePermission(rbac.PermissionMarketingWrite)).Group(func(write chi.Router) {
		write.Post("/loyalty/programs", handlers.publishLoyaltyProgram)
		write.Post("/customers/{customerID}/loyalty/evaluate", handlers.evaluateLoyalty)
	})
}

func (handlers *AdminHandlers) referralReviews(writer http.ResponseWriter, request *http.Request) {
	reviews, err := handlers.operations.SearchReferralReviews(
		request.Context(), query(request, "state"),
		query(request, "signalled") == "true", int32(queryInt(request, "pageSize")),
	)
	handlers.respond(writer, request, map[string]any{"items": reviews}, err)
}

// reviewReferral records a person's decision.
//
// Rejecting reverses every live reward through a compensating ledger
// transaction, which is why it demands a reason: it takes money back from a
// customer, and the reason is what an operator quotes to them.
func (handlers *AdminHandlers) reviewReferral(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		State string `json:"state"`
		Note  string `json:"note"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	review, err := handlers.operations.ReviewReferral(
		request.Context(), chi.URLParam(request, "customerID"),
		body.State, body.Note, actorFrom(request),
	)
	handlers.respond(writer, request, review, err)
}

func (handlers *AdminHandlers) loyaltyPrograms(writer http.ResponseWriter, request *http.Request) {
	programs, err := handlers.operations.LoyaltyPrograms(
		request.Context(), int32(queryInt(request, "pageSize")),
	)
	handlers.respond(writer, request, map[string]any{"items": programs}, err)
}

// publishLoyaltyProgram writes a new version.
//
// There is no update route, and that is the design: a customer who reached a
// tier under one set of thresholds should not fall out of it because somebody
// edited the numbers.
func (handlers *AdminHandlers) publishLoyaltyProgram(
	writer http.ResponseWriter, request *http.Request,
) {
	var body struct {
		Metric     string                `json:"metric"`
		Currency   string                `json:"currency"`
		WindowDays int32                 `json:"windowDays"`
		GraceDays  int32                 `json:"graceDays"`
		Enable     bool                  `json:"enable"`
		Tiers      []panelpg.LoyaltyTier `json:"tiers"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	program, err := handlers.operations.PublishLoyaltyProgram(request.Context(), panelpg.LoyaltyProgram{
		Metric: body.Metric, Currency: body.Currency,
		WindowDays: body.WindowDays, GraceDays: body.GraceDays, Tiers: body.Tiers,
	}, body.Enable, actorFrom(request))
	handlers.respond(writer, request, program, err)
}

func (handlers *AdminHandlers) customerLoyalty(writer http.ResponseWriter, request *http.Request) {
	standing, history, err := handlers.operations.CustomerLoyalty(
		request.Context(), chi.URLParam(request, "customerID"), 50,
	)
	handlers.respond(writer, request, map[string]any{
		"standing": standing, "history": history,
	}, err)
}

// evaluateLoyalty re-places one customer under the definition in force.
//
// It is idempotent and writes history only when the standing actually changed,
// so an operator clicking it twice produces one entry rather than two.
func (handlers *AdminHandlers) evaluateLoyalty(writer http.ResponseWriter, request *http.Request) {
	standing, err := handlers.operations.EvaluateLoyalty(
		request.Context(), chi.URLParam(request, "customerID"), time.Now().UTC(), actorFrom(request),
	)
	handlers.respond(writer, request, standing, err)
}
