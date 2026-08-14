package accountsupport

import (
	"context"
	"time"
)

// What the customer actually received.
//
// The preferences screen above this one says what they are meant to receive.
// That is a setting, not evidence, and it leaves "I never got the expiry
// warning" as a claim nobody in the conversation can check — least of all the
// customer, who has only the absence of a message to go on.
//
// Everything needed was already recorded. This reads it back.
//
// Two things are deliberately not here. There is no message body: the row says
// that a notice of a kind happened, not what it said, and reconstructing text
// from a template and stale data would show the customer something that was
// never sent. And there is no other customer's history — the query is bound to
// the session's own identifier at the only place that has one.

// Delivery is one notification as its recipient sees it.
type Delivery struct {
	Kind   string
	Status string
	// Reason explains a status that is not `sent`. The codes an installation
	// produces on purpose — `quiet_hours`, `frequency_cap`, `no_consent` — are
	// the useful half: they turn "nothing arrived" into "your own setting held
	// it back", which is something the customer can act on.
	Reason string

	ScheduledAt   time.Time
	SentAt        time.Time
	DeferredUntil time.Time

	// SubscriptionSlot and SubscriptionLabel name which subscription a notice
	// was about, because "your subscription expires soon" means little to
	// somebody holding three.
	SubscriptionSlot  int32
	SubscriptionLabel string
}

// deliveryHistoryLimit bounds what one customer can read back.
//
// Ninety days of one busy account is well inside it, and history is read by
// somebody looking for a specific message rather than exported.
const deliveryHistoryLimit = 100

// Deliveries reads the customer's own notification history, newest first.
func (service *Service) Deliveries(
	ctx context.Context, customerID string, limit int,
) ([]Delivery, error) {
	if limit <= 0 || limit > deliveryHistoryLimit {
		limit = deliveryHistoryLimit
	}
	rows, err := service.pool.Query(ctx, `
		SELECT d.kind, d.status, COALESCE(d.error_code, ''),
		       d.scheduled_at, d.sent_at, d.deferred_until,
		       COALESCE(s.slot, 0), COALESCE(s.label, '')
		FROM notification_deliveries d
		LEFT JOIN subscriptions s ON s.id = d.subscription_id
		WHERE d.user_id = $1::uuid
		ORDER BY COALESCE(d.sent_at, d.scheduled_at) DESC, d.id
		LIMIT $2`, customerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	deliveries := make([]Delivery, 0, limit)
	for rows.Next() {
		var delivery Delivery
		var sentAt, deferredUntil *time.Time
		if err := rows.Scan(
			&delivery.Kind, &delivery.Status, &delivery.Reason,
			&delivery.ScheduledAt, &sentAt, &deferredUntil,
			&delivery.SubscriptionSlot, &delivery.SubscriptionLabel,
		); err != nil {
			return nil, err
		}
		if sentAt != nil {
			delivery.SentAt = *sentAt
		}
		if deferredUntil != nil {
			delivery.DeferredUntil = *deferredUntil
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, rows.Err()
}
