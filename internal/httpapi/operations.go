package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/platform"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// operations carries the runtime surfaces the operational endpoints need. They
// are optional: an installation without a job client simply does not expose the
// job endpoints.
type operations struct {
	pool  *pgxpool.Pool
	jobs  *river.Client[pgx.Tx]
	store *commercepg.Store
}

// WithOperations enables the operational endpoints: job and dead-letter
// visibility, webhook inspection, outbox lag, backup status, and the manual
// maintenance switch.
func (handlers *CommerceHandlers) WithOperations(pool *pgxpool.Pool, jobs *river.Client[pgx.Tx], store *commercepg.Store) *CommerceHandlers {
	handlers.operations = &operations{pool: pool, jobs: jobs, store: store}
	return handlers
}

func (handlers *CommerceHandlers) mountOperations(admin chi.Router) {
	if handlers.operations == nil {
		return
	}
	admin.Get("/jobs", handlers.listJobs)
	admin.Post("/jobs/{jobID}/retry", handlers.retryJob)
	admin.Post("/jobs/{jobID}/cancel", handlers.cancelJob)
	admin.Get("/webhooks", handlers.listWebhookEvents)
	admin.Get("/outbox", handlers.outboxStatus)
	admin.Get("/backups", handlers.listBackups)
	admin.Get("/maintenance", handlers.getMaintenance)
	admin.Put("/maintenance", handlers.setMaintenance)
}

// jobStates maps the query filter onto River states. "dead_letter" is the
// operator-facing name for jobs that will not be retried again on their own.
var jobStates = map[string][]rivertype.JobState{
	"dead_letter": {rivertype.JobStateDiscarded, rivertype.JobStateCancelled},
	"retryable":   {rivertype.JobStateRetryable},
	"running":     {rivertype.JobStateRunning},
	"available":   {rivertype.JobStateAvailable},
	"scheduled":   {rivertype.JobStateScheduled},
	"completed":   {rivertype.JobStateCompleted},
}

func (handlers *CommerceHandlers) listJobs(writer http.ResponseWriter, request *http.Request) {
	if handlers.operations.jobs == nil {
		writeProblem(writer, request, 503, "jobs_unavailable", "This process does not have a job client")
		return
	}
	filter := request.URL.Query().Get("state")
	if filter == "" {
		filter = "dead_letter"
	}
	states, known := jobStates[filter]
	if !known {
		writeProblem(writer, request, 400, "invalid_state", "Unknown job state filter")
		return
	}
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	result, err := handlers.operations.jobs.JobList(request.Context(), river.NewJobListParams().States(states...).First(limit))
	if err != nil {
		writeProblem(writer, request, 500, "jobs_unavailable", "Job list is unavailable")
		return
	}
	items := make([]map[string]any, 0, len(result.Jobs))
	for _, job := range result.Jobs {
		items = append(items, jobResponse(job))
	}
	writeJSON(writer, 200, map[string]any{"state": filter, "items": items})
}

// jobResponse projects a job for an operator. Job arguments are summarised
// rather than echoed, because they carry identifiers that belong behind the
// records they point at, and errors are redacted before they leave the process.
func jobResponse(job *rivertype.JobRow) map[string]any {
	lastError := ""
	if len(job.Errors) > 0 {
		lastError = platform.Redact(job.Errors[len(job.Errors)-1].Error)
	}
	response := map[string]any{
		"id": job.ID, "kind": job.Kind, "queue": job.Queue, "state": string(job.State),
		"attempt": job.Attempt, "maxAttempts": job.MaxAttempts, "createdAt": job.CreatedAt,
		"errorCount": len(job.Errors), "lastError": lastError,
	}
	if job.AttemptedAt != nil {
		response["attemptedAt"] = *job.AttemptedAt
	}
	if job.FinalizedAt != nil {
		response["finalizedAt"] = *job.FinalizedAt
	}
	return response
}

func (handlers *CommerceHandlers) retryJob(writer http.ResponseWriter, request *http.Request) {
	handlers.mutateJob(writer, request, func(id int64) (*rivertype.JobRow, error) {
		return handlers.operations.jobs.JobRetry(request.Context(), id)
	})
}

func (handlers *CommerceHandlers) cancelJob(writer http.ResponseWriter, request *http.Request) {
	handlers.mutateJob(writer, request, func(id int64) (*rivertype.JobRow, error) {
		return handlers.operations.jobs.JobCancel(request.Context(), id)
	})
}

// mutateJob applies a retry or a cancellation. River's own operations are
// idempotent for a job already in the requested state, and the underlying
// fulfillment operation carries its own idempotency key, so a retried job can
// never repeat a side effect that already succeeded.
func (handlers *CommerceHandlers) mutateJob(writer http.ResponseWriter, request *http.Request, apply func(int64) (*rivertype.JobRow, error)) {
	if handlers.operations.jobs == nil {
		writeProblem(writer, request, 503, "jobs_unavailable", "This process does not have a job client")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(request, "jobID"), 10, 64)
	if err != nil {
		writeProblem(writer, request, 400, "invalid_job", "Invalid job ID")
		return
	}
	job, err := apply(id)
	if err != nil {
		writeProblem(writer, request, 422, "job_mutation_rejected", "Job could not be changed in its current state")
		return
	}
	writeJSON(writer, 200, jobResponse(job))
}

func (handlers *CommerceHandlers) listWebhookEvents(writer http.ResponseWriter, request *http.Request) {
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := handlers.operations.pool.Query(request.Context(), `SELECT id::text, provider, provider_event_id,
		signature_valid, status, error_code, received_at, processed_at
		FROM provider_webhook_events ORDER BY received_at DESC LIMIT $1`, limit)
	if err != nil {
		writeProblem(writer, request, 500, "webhooks_unavailable", "Webhook history is unavailable")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	for rows.Next() {
		var (
			id, provider, eventID, status string
			signatureValid                bool
			errorCode                     *string
			receivedAt                    time.Time
			processedAt                   *time.Time
		)
		if err = rows.Scan(&id, &provider, &eventID, &signatureValid, &status, &errorCode, &receivedAt, &processedAt); err != nil {
			writeProblem(writer, request, 500, "webhooks_unavailable", "Webhook history is unavailable")
			return
		}
		// The raw body is deliberately never returned: it is the provider's own
		// payload and can contain payment details.
		items = append(items, map[string]any{
			"id": id, "provider": provider, "providerEventId": eventID,
			"signatureValid": signatureValid, "status": status, "errorCode": errorCode,
			"receivedAt": receivedAt, "processedAt": processedAt,
		})
	}
	writeJSON(writer, 200, map[string]any{"items": items})
}

func (handlers *CommerceHandlers) outboxStatus(writer http.ResponseWriter, request *http.Request) {
	status, err := dbgen.New(handlers.operations.pool).CountUnpublishedOutboxEvents(request.Context())
	if err != nil {
		writeProblem(writer, request, 500, "outbox_unavailable", "Outbox status is unavailable")
		return
	}
	writeJSON(writer, 200, map[string]any{"pendingCount": status.PendingCount, "oldestAgeSeconds": status.OldestAgeSeconds})
}

func (handlers *CommerceHandlers) listBackups(writer http.ResponseWriter, request *http.Request) {
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	records, err := dbgen.New(handlers.operations.pool).ListBackups(request.Context(), int32(limit))
	if err != nil {
		writeProblem(writer, request, 500, "backups_unavailable", "Backup history is unavailable")
		return
	}
	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		items = append(items, map[string]any{
			"id": uuidString(record.ID), "kind": record.Kind, "status": record.Status,
			"fileName": record.FileName, "sizeBytes": record.SizeBytes, "encrypted": record.Encrypted,
			"startedAt": record.StartedAt.Time, "completedAt": nullableTime(record.CompletedAt),
			"verifiedAt": nullableTime(record.VerifiedAt), "retainUntil": record.RetainUntil.Time,
			"errorCode": nullableText(record.ErrorCode),
		})
	}
	writeJSON(writer, 200, map[string]any{"items": items})
}

func (handlers *CommerceHandlers) getMaintenance(writer http.ResponseWriter, request *http.Request) {
	state, err := handlers.operations.store.Maintenance(request.Context())
	if err != nil {
		writeProblem(writer, request, 500, "maintenance_unavailable", "Maintenance state is unavailable")
		return
	}
	writeJSON(writer, 200, maintenanceResponse(state))
}

// setMaintenance is the manual switch. Automatic detection may still clear a
// window it opened itself, but it never clears one an operator opened.
func (handlers *CommerceHandlers) setMaintenance(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Active                     bool
		Reason, NoticeRU, NoticeEN string
		OperatorID                 string
		ExpectedReturnAt           *time.Time
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if body.OperatorID == "" {
		writeProblem(writer, request, 400, "missing_operator", "operatorId is required")
		return
	}
	desired := commerce.Maintenance{
		Active: body.Active, Source: commerce.MaintenanceManual, Reason: body.Reason,
		NoticeRU: body.NoticeRU, NoticeEN: body.NoticeEN,
	}
	if body.ExpectedReturnAt != nil {
		desired.ExpectedReturnAt = *body.ExpectedReturnAt
	}
	state, err := handlers.operations.store.SetMaintenance(request.Context(), desired, "operator", body.OperatorID)
	if err != nil {
		writeProblem(writer, request, 422, "maintenance_rejected", err.Error())
		return
	}
	writeJSON(writer, 200, maintenanceResponse(state))
}

func maintenanceResponse(state commerce.Maintenance) map[string]any {
	response := map[string]any{
		"active": state.Active, "source": state.Source, "reason": state.Reason,
		"noticeRu": state.NoticeRU, "noticeEn": state.NoticeEN,
	}
	if !state.ActivatedAt.IsZero() {
		response["activatedAt"] = state.ActivatedAt
	}
	if !state.ExpectedReturnAt.IsZero() {
		response["expectedReturnAt"] = state.ExpectedReturnAt
	}
	return response
}
