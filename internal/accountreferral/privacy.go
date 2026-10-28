package accountreferral

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// The audit actions this surface writes. They are the record of what a customer
// asked for, kept where an operator can be held to it.
const (
	actionDeletionRequested = "account.deletion.requested"
	actionDeletionCancelled = "account.deletion.cancelled"
)

// DeletionExecutor names what actually performs a deletion.
//
// It is a constant in the response rather than prose, so the panel can explain
// the workflow in the customer's own language while the API stays the single
// statement of fact: this request is reviewed and executed by the retention
// workflow an operator governs, not by the browser that made it.
const DeletionExecutor = "operator_retention_workflow"

// Privacy is the privacy screen in one response.
type Privacy struct {
	Retention Retention
	Deletion  Deletion
	Consents  ConsentSummary
	// Export describes what an export would contain, before one is asked for.
	Export ExportPreview
}

// Retention is the account's own lifecycle state.
type Retention struct {
	Status         string
	SuspendedAt    *time.Time
	DeletedAt      *time.Time
	AnonymizedAt   *time.Time
	RetentionUntil *time.Time
}

// Deletion is the state of the customer's deletion request.
type Deletion struct {
	Pending     bool
	RequestedAt *time.Time
	Reason      string
	// CancelledAt is the withdrawal of the most recent request, kept so a
	// customer who changed their mind twice can see what the current state is.
	CancelledAt *time.Time
	// ExecutedBy is always DeletionExecutor. It is carried explicitly because
	// the difference between "requested" and "done" is the entire safety
	// property of this route, and a screen that does not say so invites a
	// customer to believe their data is already gone.
	ExecutedBy string
}

// ConsentSummary is the customer's consent trail, latest decision per purpose.
type ConsentSummary struct {
	// Current maps a purpose to the most recent decision, which is what a
	// preferences screen renders.
	Current map[string]bool
	// History is the dated trail, newest first, bounded to a readable page.
	History []ExportConsent
}

// ExportPreview describes an export without producing one.
type ExportPreview struct {
	Sections   []string
	Redactions []string
	// ContactValuesAvailable is false when no contact encryption key is
	// configured, in which case the contacts section lists channels without
	// their addresses.
	ContactValuesAvailable bool
}

// Privacy reads the privacy screen.
func (service *Service) Privacy(ctx context.Context, customerID string) (Privacy, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return Privacy{}, err
	}

	privacy := Privacy{
		Export: ExportPreview{
			Sections: ExportSections(), Redactions: ExportRedactions(),
			ContactValuesAvailable: service.ContactsAvailable(),
		},
	}

	user, err := service.queries.GetCustomer(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Privacy{}, ErrNotFound
	}
	if err != nil {
		return Privacy{}, err
	}
	privacy.Retention.Status = user.Status
	privacy.Retention.SuspendedAt = timePointer(user.SuspendedAt)
	privacy.Retention.DeletedAt = timePointer(user.DeletedAt)
	privacy.Retention.AnonymizedAt = timePointer(user.AnonymizedAt)
	privacy.Retention.RetentionUntil = timePointer(user.RetentionUntil)

	if privacy.Deletion, err = service.deletionState(ctx, service.queries, userID, customerID); err != nil {
		return Privacy{}, err
	}
	if privacy.Consents, err = service.consents(ctx, userID); err != nil {
		return Privacy{}, err
	}
	return privacy, nil
}

// RequestDeletion records the customer's request and tells the operator.
//
// Nothing is deleted, suspended, or anonymized here. The row appended to
// `customer_lifecycle_events` is a request with the customer as its actor; the
// retention workflow an operator already governs is what carries it out. An
// irreversible action must never happen on the strength of one browser session,
// however recently that session authenticated — recent authentication is what
// stops a borrowed laptop from making the request, not a licence to execute it.
//
// What makes the request more than a line in a log is the operator notice
// queued in the same transaction: a request nobody is told about is a request
// nobody acts on. The notice names the event and the customer's identifier and
// nothing else — not the reason, which is the customer's own words and belongs
// in the record the operator opens, not in a group chat.
func (service *Service) RequestDeletion(
	ctx context.Context, customerID, reason string, request RequestContext,
) (Deletion, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return Deletion{}, err
	}
	reason = strings.TrimSpace(reason)
	switch {
	case reason == "":
		// The reason is required by the table and useful to the operator who
		// reviews the request, but the value is the customer's own words rather
		// than a menu: a closed list would put the operator's categories in the
		// customer's mouth.
		return Deletion{}, invalid("a reason is required")
	case len(reason) > 400:
		return Deletion{}, invalid("that reason is too long")
	}

	transaction, err := service.pool.Begin(ctx)
	if err != nil {
		return Deletion{}, err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	queries := service.queries.WithTx(transaction)

	current, err := service.deletionState(ctx, queries, userID, customerID)
	if err != nil {
		return Deletion{}, err
	}
	if current.Pending {
		// Repeating the request changes nothing and adds noise to the record an
		// operator reads before acting on it.
		return Deletion{}, ErrDeletionPending
	}

	event, err := queries.InsertCustomerLifecycleEvent(ctx, dbgen.InsertCustomerLifecycleEventParams{
		UserID:    userID,
		Action:    "deletion_requested",
		Reason:    reason,
		ActorType: "customer",
		ActorID:   pgtype.Text{String: customerID, Valid: true},
		RequestID: optionalText(request.RequestID),
	})
	if err != nil {
		return Deletion{}, err
	}
	requestedAt := event.OccurredAt
	if err = service.recordAudit(ctx, queries, customerID, actionDeletionRequested, request.RequestID,
		map[string]any{"executedBy": DeletionExecutor},
	); err != nil {
		return Deletion{}, err
	}
	if err = notifyOperatorOfDeletion(ctx, queries, customerID, requestedAt.Time); err != nil {
		return Deletion{}, err
	}
	if err = transaction.Commit(ctx); err != nil {
		return Deletion{}, err
	}

	moment := requestedAt.Time.UTC()
	return Deletion{
		Pending: true, RequestedAt: &moment, Reason: reason, ExecutedBy: DeletionExecutor,
	}, nil
}

// notifyOperatorOfDeletion queues the operator notice for a deletion request.
//
// It rides the `security` stream because that is the stream an operator
// watches for account-level events, and because the set of streams is fixed by
// the schema. The dedupe key is the customer and the moment, so a retried
// transaction cannot queue two notices for one request and a later request
// after a withdrawal is a new notice.
func notifyOperatorOfDeletion(ctx context.Context, queries *dbgen.Queries, customerID string, at time.Time) error {
	payload, err := json.Marshal(map[string]any{
		"event":      "customer_deletion_requested",
		"status":     "awaiting_operator",
		"customerId": customerID,
	})
	if err != nil {
		return err
	}
	_, err = queries.EnqueueOperatorNotification(ctx, dbgen.EnqueueOperatorNotificationParams{
		Kind:      "security",
		DedupeKey: fmt.Sprintf("customer_deletion_requested:%s:%d", customerID, at.UTC().Unix()),
		Payload:   payload,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// CancelDeletion withdraws a pending request.
//
// The lifecycle event is not removed. It is an identity record and identity
// records are append-only: "this was asked for and then withdrawn" is a
// different fact from "this was never asked for", and an operator reviewing a
// later request deserves to see both. The withdrawal is recorded alongside it as
// an audit event, and a request counts as pending only while no withdrawal
// follows it.
func (service *Service) CancelDeletion(
	ctx context.Context, customerID string, request RequestContext,
) (Deletion, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return Deletion{}, err
	}

	transaction, err := service.pool.Begin(ctx)
	if err != nil {
		return Deletion{}, err
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	queries := service.queries.WithTx(transaction)

	current, err := service.deletionState(ctx, queries, userID, customerID)
	if err != nil {
		return Deletion{}, err
	}
	if !current.Pending {
		return Deletion{}, ErrNoDeletionPending
	}
	if err = service.recordAudit(ctx, queries, customerID, actionDeletionCancelled, request.RequestID,
		map[string]any{"requestedAt": current.RequestedAt},
	); err != nil {
		return Deletion{}, err
	}
	if err = transaction.Commit(ctx); err != nil {
		return Deletion{}, err
	}

	cancelled, err := service.deletionState(ctx, service.queries, userID, customerID)
	if err != nil {
		return Deletion{}, err
	}
	return cancelled, nil
}

// deletionState derives whether a request is open.
//
// It compares three instants rather than reading a flag, because there is no
// flag to read: the request lives in the append-only lifecycle table and the
// withdrawal in the append-only audit table, and neither may be edited. A
// request is pending when it is the most recent of the three — newer than any
// withdrawal, and not already carried out.
func (service *Service) deletionState(
	ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID, customerID string,
) (Deletion, error) {
	state, err := queries.GetAccountDeletionState(ctx, dbgen.GetAccountDeletionStateParams{
		UserID: userID, CustomerID: customerID, CancelAction: actionDeletionCancelled,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Deletion{ExecutedBy: DeletionExecutor}, nil
	}
	if err != nil {
		return Deletion{}, err
	}

	deletion := Deletion{
		RequestedAt: timePointer(state.OccurredAt), Reason: state.Reason,
		CancelledAt: timePointer(state.CancelledAt), ExecutedBy: DeletionExecutor,
	}
	deletion.Pending = deletionPending(state.OccurredAt, state.CancelledAt, state.CompletedAt)
	if !deletion.Pending {
		// A withdrawn or already-executed request keeps its timestamps but not
		// its reason: the customer's words were written for a decision that is no
		// longer open, and repeating them on the screen implies it still is.
		deletion.Reason = ""
	}
	return deletion, nil
}

// deletionPending is the comparison itself, separated so the ordering rule can
// be read without the query around it.
func deletionPending(requestedAt, cancelledAt, completedAt pgtype.Timestamptz) bool {
	if !requestedAt.Valid {
		return false
	}
	if cancelledAt.Valid && !cancelledAt.Time.Before(requestedAt.Time) {
		return false
	}
	if completedAt.Valid && !completedAt.Time.Before(requestedAt.Time) {
		return false
	}
	return true
}

// consents reads the trail and the current position in it.
func (service *Service) consents(ctx context.Context, userID pgtype.UUID) (ConsentSummary, error) {
	history, err := service.consentHistory(ctx, userID, defaultPageSize)
	if err != nil {
		return ConsentSummary{}, err
	}

	summary := ConsentSummary{Current: map[string]bool{}, History: make([]ExportConsent, 0, defaultPageSize)}
	for _, record := range history {
		// Newest first, so the first decision seen for a purpose is the current
		// one and later rows are history.
		if _, seen := summary.Current[record.Purpose]; !seen {
			summary.Current[record.Purpose] = record.Granted
		}
		summary.History = append(summary.History, record)
	}
	return summary, nil
}

// consentHistory reads the dated trail, newest first, bounded to limit. The
// privacy screen and the export share it so the two can never disagree about
// what a decision looked like.
func (service *Service) consentHistory(
	ctx context.Context, userID pgtype.UUID, limit int,
) ([]ExportConsent, error) {
	rows, err := service.queries.ListAccountConsentHistory(ctx, dbgen.ListAccountConsentHistoryParams{
		UserID: userID, PageSize: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	history := make([]ExportConsent, 0, len(rows))
	for _, row := range rows {
		history = append(history, ExportConsent{
			Purpose: row.Purpose, Granted: row.Granted,
			PolicyVersion: row.PolicyVersion, Source: row.Source,
			OccurredAt: row.OccurredAt.Time.UTC(),
		})
	}
	return history, nil
}
