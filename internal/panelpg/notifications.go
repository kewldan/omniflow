package panelpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Notification history, and a test notification.
//
// The preferences screen has always said what a customer is meant to receive.
// Nothing said what they actually got, which makes "I never got it" a claim
// nobody can check — the customer cannot, the operator cannot, and the
// conversation ends in a guess about whether the bot works.
//
// It was never a recording problem. `notification_deliveries` has held the
// answer since the first release: the kind, the class, whether it left, when,
// how many attempts it took, and — when it never left — a code saying why. The
// codes are the point. `quiet_hours`, `frequency_cap`, and `no_consent` are
// deliveries the installation declined to make on purpose, and telling somebody
// "your marketing preference is off, so we did not send it" is a different
// conversation from "we have no idea".
//
// The test send exists because history only answers questions about the past.
// An operator setting up an installation, or looking at a customer who says
// nothing arrives, needs to make one message happen now.

// Delivery is one recorded notification.
type Delivery struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Class  string `json:"class"`
	Status string `json:"status"`

	ScheduledAt   time.Time `json:"scheduledAt"`
	SentAt        time.Time `json:"sentAt,omitzero"`
	DeferredUntil time.Time `json:"deferredUntil,omitzero"`
	FailureCount  int32     `json:"failureCount"`

	// Reason is `error_code`: a transport failure such as `bot_blocked`, or a
	// policy outcome such as `quiet_hours`. Empty when the message was sent.
	Reason string `json:"reason,omitempty"`

	// SubscriptionSlot and SubscriptionLabel name which subscription a
	// per-subscription notice was about. A customer holding three of them
	// cannot otherwise tell which one was expiring.
	SubscriptionID    string `json:"subscriptionId,omitempty"`
	SubscriptionSlot  int32  `json:"subscriptionSlot,omitempty"`
	SubscriptionLabel string `json:"subscriptionLabel,omitempty"`
}

// DeliveryPage is one page of history with the total behind it.
type DeliveryPage struct {
	Deliveries []Delivery `json:"deliveries"`
	Total      int64      `json:"total"`
}

// DeliverySummary is one kind's totals across the whole history.
type DeliverySummary struct {
	Kind       string    `json:"kind"`
	Total      int64     `json:"total"`
	Sent       int64     `json:"sent"`
	Failed     int64     `json:"failed"`
	Suppressed int64     `json:"suppressed"`
	Waiting    int64     `json:"waiting"`
	LastSentAt time.Time `json:"lastSentAt,omitzero"`
}

// QueuedTest is what a test send reports back.
type QueuedTest struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	// Queued is false when an identical test was already waiting to go out,
	// which is what a double-click produces. Nothing new was created and the
	// customer still receives exactly one message.
	Queued      bool      `json:"queued"`
	ScheduledAt time.Time `json:"scheduledAt,omitzero"`
}

// deliveryPageSize bounds one request. History is read by a person looking for
// a specific message, not exported.
const deliveryPageSize = 50

// Deliveries reads one customer's notification history.
//
// The filters are the two questions actually asked of it: "did the expiry
// notice go out" and "what has failed". Both narrow rather than search, because
// there is no free text on a delivery to search — the row records that a
// message of a kind happened, not what it said.
func (service *Service) Deliveries(
	ctx context.Context, customerID, kind, status string, offset, size int32,
) (DeliveryPage, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return DeliveryPage{}, err
	}
	if size <= 0 || size > deliveryPageSize {
		size = deliveryPageSize
	}
	if offset < 0 {
		offset = 0
	}
	kindFilter, statusFilter := optionalText(kind), optionalText(status)

	rows, err := service.queries().ListNotificationDeliveries(ctx, dbgen.ListNotificationDeliveriesParams{
		UserID: id, Kind: kindFilter, Status: statusFilter,
		PageOffset: offset, PageSize: size,
	})
	if err != nil {
		return DeliveryPage{}, err
	}
	total, err := service.queries().CountNotificationDeliveries(ctx, dbgen.CountNotificationDeliveriesParams{
		UserID: id, Kind: kindFilter, Status: statusFilter,
	})
	if err != nil {
		return DeliveryPage{}, err
	}

	page := DeliveryPage{Deliveries: make([]Delivery, 0, len(rows)), Total: total}
	for _, row := range rows {
		page.Deliveries = append(page.Deliveries, Delivery{
			ID: uuidString(row.ID), Kind: row.Kind, Class: row.Class, Status: row.Status,
			ScheduledAt: row.ScheduledAt.Time, SentAt: instant(row.SentAt),
			DeferredUntil: instant(row.DeferredUntil), FailureCount: row.FailureCount,
			Reason:            row.ErrorCode.String,
			SubscriptionID:    uuidString(row.SubscriptionID),
			SubscriptionSlot:  row.Slot.Int32,
			SubscriptionLabel: row.SubscriptionLabel,
		})
	}
	return page, nil
}

// DeliverySummaries is the shape of a customer's whole notification history in
// a handful of rows, which is what somebody wants before reading any of it.
func (service *Service) DeliverySummaries(
	ctx context.Context, customerID string,
) ([]DeliverySummary, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().SummariseNotificationDeliveries(ctx, id)
	if err != nil {
		return nil, err
	}
	summaries := make([]DeliverySummary, 0, len(rows))
	for _, row := range rows {
		summaries = append(summaries, DeliverySummary{
			Kind: row.Kind, Total: row.Total, Sent: row.Sent, Failed: row.Failed,
			Suppressed: row.Suppressed, Waiting: row.Waiting,
			LastSentAt: instant(row.LastSentAt),
		})
	}
	return summaries, nil
}

// SendTestNotification queues one message for a customer through the ordinary
// delivery path.
//
// It is queued rather than sent inline, and that is the whole value of it. The
// panel process holds no Telegram connection; the notifier does. A test that
// took a different road would prove the test's road works and tell you nothing
// about the one real notifications travel. This one is claimed by the same
// worker, sent by the same call, and recorded in the same table with the same
// failure codes — so when it fails, it fails the way the real ones are failing.
//
// It carries its own kind so it can never be mistaken for a real notice in the
// history and can never spend a marketing frequency budget.
func (service *Service) SendTestNotification(
	ctx context.Context, customerID string, actor Actor,
) (QueuedTest, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return QueuedTest{}, err
	}

	// The dedupe key is the operator and the minute. Two operators testing the
	// same customer are two messages, because they are two questions; one
	// operator clicking twice is one, because it is one.
	key := fmt.Sprintf("%s:%s", actor.AdminID, time.Now().UTC().Format("2006-01-02T15:04"))

	var queued QueuedTest
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, queueErr := queries.QueueTestNotification(ctx, dbgen.QueueTestNotificationParams{
			UserID: id, DedupeKey: key,
		})
		switch {
		case errors.Is(queueErr, pgx.ErrNoRows):
			// The conflict clause declined to touch a row that is already
			// pending. Nothing failed; the message is already on its way.
			queued = QueuedTest{Status: "pending", Queued: false}
		case queueErr != nil:
			return notFound(queueErr)
		default:
			queued = QueuedTest{
				ID: uuidString(row.ID), Status: row.Status, Queued: true,
				ScheduledAt: row.ScheduledAt.Time,
			}
		}
		return appendAudit(ctx, queries, actor.audit(
			"customer.notification.tested", "support", "customer", customerID,
			map[string]any{"queued": queued.Queued},
		))
	})
	return queued, err
}

// instant unwraps a nullable timestamp into the zero instant, which every
// caller here already treats as "did not happen".
func instant(at pgtype.Timestamptz) time.Time {
	if !at.Valid {
		return time.Time{}
	}
	return at.Time
}
