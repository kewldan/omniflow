package accountreferral

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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

	var (
		suspendedAt    pgtype.Timestamptz
		deletedAt      pgtype.Timestamptz
		anonymizedAt   pgtype.Timestamptz
		retentionUntil pgtype.Timestamptz
	)
	err = service.pool.QueryRow(ctx, `SELECT status, suspended_at, deleted_at, anonymized_at, retention_until
		FROM users WHERE id = $1`, userID).
		Scan(&privacy.Retention.Status, &suspendedAt, &deletedAt, &anonymizedAt, &retentionUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return Privacy{}, ErrNotFound
	}
	if err != nil {
		return Privacy{}, err
	}
	privacy.Retention.SuspendedAt = timePointer(suspendedAt)
	privacy.Retention.DeletedAt = timePointer(deletedAt)
	privacy.Retention.AnonymizedAt = timePointer(anonymizedAt)
	privacy.Retention.RetentionUntil = timePointer(retentionUntil)

	if privacy.Deletion, err = service.deletionState(ctx, service.pool, userID, customerID); err != nil {
		return Privacy{}, err
	}
	if privacy.Consents, err = service.consents(ctx, userID); err != nil {
		return Privacy{}, err
	}
	return privacy, nil
}

// RequestDeletion records the customer's request and nothing more.
//
// Nothing is deleted, suspended, or anonymized here. The row appended to
// `customer_lifecycle_events` is a request with the customer as its actor; the
// retention workflow an operator already governs is what carries it out. An
// irreversible action must never happen on the strength of one browser session,
// however recently that session authenticated — recent authentication is what
// stops a borrowed laptop from making the request, not a licence to execute it.
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

	current, err := service.deletionState(ctx, transaction, userID, customerID)
	if err != nil {
		return Deletion{}, err
	}
	if current.Pending {
		// Repeating the request changes nothing and adds noise to the record an
		// operator reads before acting on it.
		return Deletion{}, ErrDeletionPending
	}

	var requestedAt pgtype.Timestamptz
	if err = transaction.QueryRow(ctx, `INSERT INTO customer_lifecycle_events
			(user_id, action, reason, actor_type, actor_id, request_id)
		VALUES ($1, 'deletion_requested', $2, 'customer', $3, $4)
		RETURNING occurred_at`,
		userID, reason, customerID, optionalText(request.RequestID)).
		Scan(&requestedAt); err != nil {
		return Deletion{}, err
	}
	if err = service.recordAudit(ctx, transaction, customerID, actionDeletionRequested, request.RequestID,
		map[string]any{"executedBy": DeletionExecutor},
	); err != nil {
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

	current, err := service.deletionState(ctx, transaction, userID, customerID)
	if err != nil {
		return Deletion{}, err
	}
	if !current.Pending {
		return Deletion{}, ErrNoDeletionPending
	}
	if err = service.recordAudit(ctx, transaction, customerID, actionDeletionCancelled, request.RequestID,
		map[string]any{"requestedAt": current.RequestedAt},
	); err != nil {
		return Deletion{}, err
	}
	if err = transaction.Commit(ctx); err != nil {
		return Deletion{}, err
	}

	cancelled, err := service.deletionState(ctx, service.pool, userID, customerID)
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
	ctx context.Context, executor executor, userID pgtype.UUID, customerID string,
) (Deletion, error) {
	var (
		requestedAt pgtype.Timestamptz
		reason      pgtype.Text
		cancelledAt pgtype.Timestamptz
		completedAt pgtype.Timestamptz
	)
	err := executor.QueryRow(ctx, `SELECT
			request.occurred_at, request.reason,
			(SELECT max(a.occurred_at) FROM audit_events a
				WHERE a.actor_type = 'customer' AND a.target_type = 'customer'
				  AND a.target_id = $2 AND a.action = $3),
			(SELECT max(e.occurred_at) FROM customer_lifecycle_events e
				WHERE e.user_id = $1 AND e.action IN ('deleted', 'anonymized'))
		FROM (
			SELECT occurred_at, reason FROM customer_lifecycle_events
			WHERE user_id = $1 AND action = 'deletion_requested' AND actor_type = 'customer'
			ORDER BY occurred_at DESC LIMIT 1
		) request`, userID, customerID, actionDeletionCancelled).
		Scan(&requestedAt, &reason, &cancelledAt, &completedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Deletion{ExecutedBy: DeletionExecutor}, nil
	}
	if err != nil {
		return Deletion{}, err
	}

	deletion := Deletion{
		RequestedAt: timePointer(requestedAt), Reason: reason.String,
		CancelledAt: timePointer(cancelledAt), ExecutedBy: DeletionExecutor,
	}
	deletion.Pending = deletionPending(requestedAt, cancelledAt, completedAt)
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
	rows, err := service.pool.Query(ctx, `SELECT purpose, granted, policy_version, source, occurred_at
		FROM consent_records WHERE user_id = $1 ORDER BY occurred_at DESC LIMIT $2`,
		userID, defaultPageSize)
	if err != nil {
		return ConsentSummary{}, err
	}
	defer rows.Close()

	summary := ConsentSummary{Current: map[string]bool{}, History: make([]ExportConsent, 0, defaultPageSize)}
	for rows.Next() {
		var (
			record     ExportConsent
			occurredAt pgtype.Timestamptz
		)
		if err = rows.Scan(
			&record.Purpose, &record.Granted, &record.PolicyVersion, &record.Source, &occurredAt,
		); err != nil {
			return ConsentSummary{}, err
		}
		record.OccurredAt = occurredAt.Time.UTC()
		// Newest first, so the first decision seen for a purpose is the current
		// one and later rows are history.
		if _, seen := summary.Current[record.Purpose]; !seen {
			summary.Current[record.Purpose] = record.Granted
		}
		summary.History = append(summary.History, record)
	}
	return summary, rows.Err()
}
