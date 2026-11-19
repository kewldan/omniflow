// Package remnawavesync reacts to what Remnawave reports about itself: the
// webhooks it pushes, and the periodic reads that keep Omniflow's view of the
// panel current.
//
// It never decides anything about VPN state. A panel event becomes a notice
// for a customer or an operator, and a periodic read becomes a report; the
// panel stays authoritative for users, traffic, devices, and nodes, and any
// change Omniflow wants made still goes through a fulfillment operation.
package remnawavesync

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
)

var (
	// ErrNotConfigured reports an installation with no webhook secret. The
	// endpoint refuses every delivery rather than accept unsigned ones.
	ErrNotConfigured = errors.New("remnawave webhooks are not configured")
	// ErrBadSignature reports a delivery whose signature does not verify.
	ErrBadSignature = errors.New("remnawave webhook signature does not verify")
)

// customerEvents are the panel events a customer is told about. Everything
// else the panel reports about a user — expiry, limits, edits — is already
// covered by Omniflow's own lifecycle notices, which word it from the
// entitlement the customer paid for.
var customerEvents = map[string]bool{
	remnawave.EventFirstConnected:   true,
	remnawave.EventNotConnected:     true,
	remnawave.EventBandwidthReached: true,
	remnawave.EventTrafficReset:     true,
	remnawave.EventRevoked:          true,
	remnawave.EventTorrentBlocked:   true,
	remnawave.EventDeviceAdded:      true,
	remnawave.EventDeviceDeleted:    true,
}

// operatorScopes are the panel's own infrastructure: nodes, the panel
// service, and its infrastructure-billing reminders.
var operatorScopes = map[string]bool{"node": true, "service": true, "crm": true, "errors": true}

// Intake verifies and records webhook deliveries.
type Intake struct {
	pool       *pgxpool.Pool
	secret     string
	nodeAlerts bool
}

// IntakeOptions configure an Intake.
type IntakeOptions struct {
	// Secret is APP_REMNAWAVE_WEBHOOK_SECRET, the panel's
	// WEBHOOK_SECRET_HEADER. Empty disables the endpoint.
	Secret string
	// NodeAlerts posts node, panel, and infrastructure-billing events to the
	// operator group (APP_REMNAWAVE_WEBHOOK_NODE_ALERTS, default on).
	NodeAlerts bool
}

// IntakeOptionsFromEnv reads the webhook settings.
func IntakeOptionsFromEnv(getenv func(string) string) IntakeOptions {
	options := IntakeOptions{Secret: strings.TrimSpace(getenv("APP_REMNAWAVE_WEBHOOK_SECRET")), NodeAlerts: true}
	if value, err := strconv.ParseBool(strings.TrimSpace(getenv("APP_REMNAWAVE_WEBHOOK_NODE_ALERTS"))); err == nil {
		options.NodeAlerts = value
	}
	return options
}

// NewIntake builds the intake. It is nil-safe: a nil pool yields an intake
// that reports itself unconfigured.
func NewIntake(pool *pgxpool.Pool, options IntakeOptions) *Intake {
	return &Intake{pool: pool, secret: options.Secret, nodeAlerts: options.NodeAlerts}
}

// Configured reports whether deliveries can be accepted.
func (intake *Intake) Configured() bool {
	return intake != nil && intake.pool != nil && intake.secret != ""
}

// Outcome says what became of one delivery.
type Outcome struct {
	Event string
	// Duplicate is a retry of a delivery already recorded.
	Duplicate bool
	// Customer is an event queued for the bot to tell a customer about.
	Customer bool
	// Operator is an event posted to the operator group.
	Operator bool
}

// Accept verifies one delivery and records it. The event row and any
// operator notice it produces are written in one transaction, so a crash
// between them cannot leave a notice with no record or a record whose notice
// was lost.
func (intake *Intake) Accept(ctx context.Context, body []byte, signature string) (Outcome, error) {
	if !intake.Configured() {
		return Outcome{}, ErrNotConfigured
	}
	if !remnawave.VerifyWebhook(intake.secret, body, signature) {
		return Outcome{}, ErrBadSignature
	}
	event, err := remnawave.ParseWebhook(body)
	if err != nil {
		return Outcome{}, err
	}
	outcome := Outcome{Event: event.Event}
	status := "ignored"
	switch {
	case customerEvents[event.Event] && event.UserID > 0:
		status, outcome.Customer = "pending", true
	case operatorScopes[event.Scope] && intake.nodeAlerts:
		status, outcome.Operator = "delivered", true
	}
	// The torrent blocker is both: the customer is told their traffic was
	// blocked, and the operator that it happened on a node.
	if event.Event == remnawave.EventTorrentBlocked && intake.nodeAlerts {
		outcome.Operator = true
	}

	details, err := json.Marshal(event.Details)
	if err != nil {
		return Outcome{}, err
	}
	digest := sha256.Sum256(body)
	tx, err := intake.pool.Begin(ctx)
	if err != nil {
		return Outcome{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	id, err := queries.InsertRemnawaveWebhookEvent(ctx, dbgen.InsertRemnawaveWebhookEventParams{
		Event: event.Event, BodySha256: digest[:],
		RemnawaveUserID: pgtype.Int8{Int64: event.UserID, Valid: event.UserID > 0},
		Details:         details, Status: status,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Outcome{Event: event.Event, Duplicate: true}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	if outcome.Operator {
		kind := "node"
		if event.Event == remnawave.EventTorrentBlocked {
			kind = "monitoring"
		}
		payload := map[string]any{"event": event.Event, "source": "remnawave"}
		for _, field := range []string{"node", "country", "provider"} {
			if value, ok := event.Details[field]; ok {
				payload[field] = value
			}
		}
		if event.UserID > 0 {
			payload["remnawaveUserId"] = event.UserID
		}
		encoded, encodeErr := json.Marshal(payload)
		if encodeErr != nil {
			return Outcome{}, encodeErr
		}
		if _, err = queries.EnqueueOperatorNotification(ctx, dbgen.EnqueueOperatorNotificationParams{
			Kind: kind, DedupeKey: "remnawave:" + strconv.FormatInt(id, 10), Payload: encoded,
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Outcome{}, err
		}
	}
	return outcome, tx.Commit(ctx)
}
