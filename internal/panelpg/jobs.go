package panelpg

import (
	"context"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// FulfillmentFilter is what the job diagnostics accept.
type FulfillmentFilter struct {
	Status        string
	Operation     string
	EntitlementID string
	Cursor        string
	PageSize      int32
}

// FulfillmentOperation is one provisioning attempt against Remnawave.
type FulfillmentOperation struct {
	ID             string    `json:"id"`
	EntitlementID  string    `json:"entitlementId"`
	CustomerID     string    `json:"customerId"`
	SubscriptionID string    `json:"subscriptionId,omitempty"`
	Operation      string    `json:"operation"`
	Status         string    `json:"status"`
	AttemptCount   int32     `json:"attemptCount"`
	NextAttemptAt  time.Time `json:"nextAttemptAt"`
	// LastErrorCode is a classification, never a provider message. A Remnawave
	// response body can carry a subscription link, and an operator list is the
	// last place one should appear.
	LastErrorCode string     `json:"lastErrorCode,omitempty"`
	CorrelationID string     `json:"correlationId"`
	CreatedAt     time.Time  `json:"createdAt"`
	CompletedAt   *time.Time `json:"completedAt,omitempty"`
}

// FulfillmentPage is one page of operations.
type FulfillmentPage struct {
	Items      []FulfillmentOperation `json:"items"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

// SearchFulfillment lists provisioning operations.
//
// Filtering on status "failed" is the dead-letter view: an operation that has
// exhausted its retries sits there until an operator retries or cancels it.
func (service *Service) SearchFulfillment(
	ctx context.Context, filter FulfillmentFilter,
) (FulfillmentPage, error) {
	size := pageSize(filter.PageSize)
	cursor := DecodeCursor(filter.Cursor)

	rows, err := service.queries().SearchFulfillmentOperations(ctx, dbgen.SearchFulfillmentOperationsParams{
		CursorCreatedAt: cursor.timestamp(),
		CursorID:        cursor.uuid(),
		Status:          optionalText(filter.Status),
		Operation:       optionalText(filter.Operation),
		EntitlementID:   optionalUUID(filter.EntitlementID),
		PageSize:        size + 1,
	})
	if err != nil {
		return FulfillmentPage{}, err
	}

	page := FulfillmentPage{Items: make([]FulfillmentOperation, 0, min(len(rows), int(size)))}
	for index, row := range rows {
		if index == int(size) {
			last := rows[index-1]
			page.NextCursor = EncodeCursor(
				timeValue(last.FulfillmentOperation.CreatedAt), uuidString(last.FulfillmentOperation.ID),
			)
			break
		}
		page.Items = append(page.Items, FulfillmentOperation{
			ID:             uuidString(row.FulfillmentOperation.ID),
			EntitlementID:  uuidString(row.FulfillmentOperation.EntitlementID),
			CustomerID:     uuidString(row.UserID),
			SubscriptionID: uuidString(row.SubscriptionID),
			Operation:      row.FulfillmentOperation.Operation,
			Status:         row.FulfillmentOperation.Status,
			AttemptCount:   row.FulfillmentOperation.AttemptCount,
			NextAttemptAt:  timeValue(row.FulfillmentOperation.NextAttemptAt),
			LastErrorCode:  textValue(row.FulfillmentOperation.LastErrorCode),
			CorrelationID:  row.FulfillmentOperation.CorrelationID,
			CreatedAt:      timeValue(row.FulfillmentOperation.CreatedAt),
			CompletedAt:    timePointer(row.FulfillmentOperation.CompletedAt),
		})
	}
	return page, nil
}

// FulfillmentAttempt is one recorded exchange with Remnawave.
type FulfillmentAttempt struct {
	Status        string    `json:"status"`
	CorrelationID string    `json:"correlationId"`
	ErrorCode     string    `json:"errorCode,omitempty"`
	OccurredAt    time.Time `json:"occurredAt"`
}

// FulfillmentHistory reads the attempt history for one operation.
//
// The request and response summaries stored on each row are deliberately not
// returned: they exist for debugging inside the worker and can contain a
// subscription link. The status, the correlation identifier, and the error
// classification are what an operator needs to decide whether to retry.
func (service *Service) FulfillmentHistory(
	ctx context.Context, operationID string,
) ([]FulfillmentAttempt, error) {
	id, err := parseUUID(operationID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListFulfillmentHistoryForOperation(ctx, id)
	if err != nil {
		return nil, err
	}
	attempts := make([]FulfillmentAttempt, 0, len(rows))
	for _, row := range rows {
		attempts = append(attempts, FulfillmentAttempt{
			Status:        row.Status,
			CorrelationID: row.CorrelationID,
			ErrorCode:     textValue(row.ErrorCode),
			OccurredAt:    timeValue(row.OccurredAt),
		})
	}
	return attempts, nil
}

// RetryFulfillment puts a failed operation back in the queue.
//
// It cannot change the operation's idempotency key, so the retried attempt is
// the same operation the worker already knows how to perform exactly once. A
// succeeded operation is not retryable at all, which is why the update is
// restricted to the failed and retrying states rather than checked in Go.
func (service *Service) RetryFulfillment(
	ctx context.Context, operationID string, actor Actor,
) (FulfillmentOperation, error) {
	id, err := parseUUID(operationID)
	if err != nil {
		return FulfillmentOperation{}, err
	}

	var operation FulfillmentOperation
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.RequeueFulfillmentOperation(ctx, id)
		if txErr != nil {
			return rejected(txErr)
		}
		operation = FulfillmentOperation{
			ID:            uuidString(row.ID),
			EntitlementID: uuidString(row.EntitlementID),
			Operation:     row.Operation,
			Status:        row.Status,
			AttemptCount:  row.AttemptCount,
			NextAttemptAt: timeValue(row.NextAttemptAt),
			CorrelationID: row.CorrelationID,
			CreatedAt:     timeValue(row.CreatedAt),
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.fulfillment.retried", "system", "fulfillment_operation", operationID,
			map[string]any{"operation": row.Operation, "attemptCount": row.AttemptCount},
		))
	})
	return operation, err
}

// CancelFulfillment abandons an operation that has not succeeded.
//
// A completed provisioning is never cancellable: the customer already has what
// was bought, and retracting it from a panel click would leave the entitlement
// and Remnawave disagreeing with no record of why.
func (service *Service) CancelFulfillment(
	ctx context.Context, operationID string, actor Actor,
) error {
	id, err := parseUUID(operationID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.CancelFulfillmentOperation(ctx, id)
		if txErr != nil {
			return rejected(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.fulfillment.cancelled", "system", "fulfillment_operation", operationID,
			map[string]any{"operation": row.Operation},
		))
	})
}

// Drift is one detected divergence between Omniflow and Remnawave.
type Drift struct {
	ID              string    `json:"id"`
	EntitlementID   string    `json:"entitlementId"`
	CustomerID      string    `json:"customerId"`
	SubscriptionID  string    `json:"subscriptionId,omitempty"`
	RemnawaveUserID *int64    `json:"remnawaveUserId,omitempty"`
	Kind            string    `json:"kind"`
	Expected        []byte    `json:"expected"`
	Observed        []byte    `json:"observed"`
	DetectedAt      time.Time `json:"detectedAt"`
}

// OpenDrifts lists divergences awaiting resolution.
func (service *Service) OpenDrifts(ctx context.Context, limit int32) ([]Drift, error) {
	rows, err := service.queries().ListOpenDriftsDetailed(ctx, pageSize(limit))
	if err != nil {
		return nil, err
	}
	drifts := make([]Drift, 0, len(rows))
	for _, row := range rows {
		drifts = append(drifts, Drift{
			ID:              uuidString(row.EntitlementDrift.ID),
			EntitlementID:   uuidString(row.EntitlementDrift.EntitlementID),
			CustomerID:      uuidString(row.UserID),
			SubscriptionID:  uuidString(row.SubscriptionID),
			RemnawaveUserID: int8Pointer(row.RemnawaveUserID),
			Kind:            row.EntitlementDrift.Kind,
			Expected:        row.EntitlementDrift.Expected,
			Observed:        row.EntitlementDrift.Observed,
			DetectedAt:      timeValue(row.EntitlementDrift.DetectedAt),
		})
	}
	return drifts, nil
}

// WebhookFilter is what the webhook diagnostics accept.
type WebhookFilter struct {
	Provider string
	Status   string
	Cursor   string
	PageSize int32
}

// WebhookEvent is one received provider callback.
//
// The raw body is not present. It is retained for replay and dispute, and it is
// the one place a provider payload legitimately lives; surfacing it in a list
// would put payment payloads in front of every operator who can read one.
type WebhookEvent struct {
	ID              string     `json:"id"`
	Provider        string     `json:"provider"`
	ProviderEventID string     `json:"providerEventId"`
	SignatureValid  bool       `json:"signatureValid"`
	Status          string     `json:"status"`
	ErrorCode       string     `json:"errorCode,omitempty"`
	ReceivedAt      time.Time  `json:"receivedAt"`
	ProcessedAt     *time.Time `json:"processedAt,omitempty"`
	RetainUntil     time.Time  `json:"retainUntil"`
}

// WebhookPage is one page of webhook events.
type WebhookPage struct {
	Items      []WebhookEvent `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

// SearchWebhooks lists received provider callbacks.
func (service *Service) SearchWebhooks(ctx context.Context, filter WebhookFilter) (WebhookPage, error) {
	size := pageSize(filter.PageSize)
	cursor := DecodeCursor(filter.Cursor)

	rows, err := service.queries().SearchWebhookEvents(ctx, dbgen.SearchWebhookEventsParams{
		CursorReceivedAt: cursor.timestamp(),
		CursorID:         cursor.uuid(),
		Provider:         optionalText(filter.Provider),
		Status:           optionalText(filter.Status),
		PageSize:         size + 1,
	})
	if err != nil {
		return WebhookPage{}, err
	}

	page := WebhookPage{Items: make([]WebhookEvent, 0, min(len(rows), int(size)))}
	for index, row := range rows {
		if index == int(size) {
			last := rows[index-1]
			page.NextCursor = EncodeCursor(timeValue(last.ReceivedAt), uuidString(last.ID))
			break
		}
		page.Items = append(page.Items, WebhookEvent{
			ID:              uuidString(row.ID),
			Provider:        row.Provider,
			ProviderEventID: row.ProviderEventID,
			SignatureValid:  row.SignatureValid,
			Status:          row.Status,
			ErrorCode:       textValue(row.ErrorCode),
			ReceivedAt:      timeValue(row.ReceivedAt),
			ProcessedAt:     timePointer(row.ProcessedAt),
			RetainUntil:     timeValue(row.RetainUntil),
		})
	}
	return page, nil
}

// ReplayWebhook marks a failed or ignored event for reprocessing.
//
// Reprocessing is replay-safe because every downstream handler is keyed on the
// provider event identifier: a second pass over the same body reaches the same
// terminal state rather than applying twice. An event that was already
// processed is deliberately not replayable — there is nothing to fix, and
// re-running it is the one case where the keying would be doing all the work.
func (service *Service) ReplayWebhook(ctx context.Context, eventID string, actor Actor) error {
	id, err := parseUUID(eventID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.MarkWebhookEventForReplay(ctx, id)
		if txErr != nil {
			return rejected(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.webhook.replayed", "system", "webhook_event", eventID,
			map[string]any{"provider": row.Provider, "providerEventId": row.ProviderEventID},
		))
	})
}

// OutboxEntry is one unpublished domain event.
type OutboxEntry struct {
	ID         string    `json:"id"`
	Topic      string    `json:"topic"`
	OccurredAt time.Time `json:"occurredAt"`
	// Age is how long it has been waiting. A queue with a thousand fresh events
	// is healthy; one with a single event from an hour ago is not, and the age
	// is what tells them apart.
	Age time.Duration `json:"-"`
}

// OutboxBacklog lists events the publisher has not yet drained.
//
// The payload is not returned. An outbox payload is a domain event that can
// name a customer and an amount, and the diagnostic question this view answers
// — is the publisher running — needs only the topic and the age.
func (service *Service) OutboxBacklog(ctx context.Context, limit int32) ([]OutboxEntry, error) {
	rows, err := service.queries().ListUnpublishedOutboxEvents(ctx, pageSize(limit))
	if err != nil {
		return nil, err
	}
	now := service.now()
	entries := make([]OutboxEntry, 0, len(rows))
	for _, row := range rows {
		occurred := timeValue(row.OccurredAt)
		entries = append(entries, OutboxEntry{
			ID:         uuidString(row.ID),
			Topic:      row.Topic,
			OccurredAt: occurred,
			Age:        now.Sub(occurred),
		})
	}
	return entries, nil
}
