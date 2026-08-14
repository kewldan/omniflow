package botapp

import (
	"context"
	"errors"
	"time"
)

// The test notification.
//
// An operator asked the panel to prove that notifications reach one customer.
// The panel wrote a row and stopped there, because it holds no Telegram
// connection — the notifier does. This is the other end.
//
// The point of routing a test through the ordinary outbox rather than sending
// it inline from the panel is that anything else proves the wrong thing. A test
// that took its own path would confirm that the test path works, which nobody
// asked. This one is claimed by the same worker, sent by the same Sender,
// classified by the same failure codes, and recorded in the same table. When
// real notifications are failing, the test fails identically and says so.
//
// Deliberately excluded: quiet hours, the marketing frequency cap, and the
// per-kind preferences. A test deferred to nine in the morning answers nothing
// at the moment somebody is asking, and its class is transactional because an
// operator asked for it — this is not an unsolicited message. Delivery health
// is still respected, because a customer who blocked the bot cannot be reached
// by wanting it more, and that is exactly the finding the operator needs.

// pendingTest is one queued test send with everything needed to deliver it.
type pendingTest struct {
	DeliveryID string
	CustomerID string
	TelegramID int64
	Locale     string
}

// pendingTests claims queued test notifications.
//
// The join to `recipient` is what makes an unreachable customer visible: a
// customer with no active Telegram identity produces no row here, so the test
// stays pending and the panel shows it never went out rather than reporting a
// success nobody received.
func (store *PostgresStore) pendingTests(ctx context.Context, limit int) ([]pendingTest, error) {
	rows, err := store.pool.Query(ctx, telegramRecipients+`
		SELECT DISTINCT ON (d.id) d.id::text, d.user_id::text, recipient.telegram_id,
		CASE WHEN COALESCE(p.locale, 'auto') = 'auto' THEN u.locale ELSE p.locale END
		FROM notification_deliveries d
		JOIN users u ON u.id = d.user_id
		JOIN recipient ON recipient.user_id = d.user_id
		LEFT JOIN bot_preferences p ON p.user_id = d.user_id
		WHERE d.kind = 'test' AND d.status = 'pending' AND u.status = 'active'
		ORDER BY d.id, d.scheduled_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tests := make([]pendingTest, 0, limit)
	for rows.Next() {
		var test pendingTest
		if err := rows.Scan(&test.DeliveryID, &test.CustomerID, &test.TelegramID, &test.Locale); err != nil {
			return nil, err
		}
		tests = append(tests, test)
	}
	return tests, rows.Err()
}

// finishTest records the outcome against the same row the panel is reading.
func (store *PostgresStore) finishTest(ctx context.Context, deliveryID string, sendErr error) error {
	if sendErr == nil {
		_, err := store.pool.Exec(ctx, `UPDATE notification_deliveries
			SET status = 'sent', sent_at = now(), error_code = NULL
			WHERE id = $1::uuid`, deliveryID)
		return err
	}
	code := "telegram_unavailable"
	var classified *DeliveryError
	if errors.As(sendErr, &classified) {
		code = classified.Code
	}
	_, err := store.pool.Exec(ctx, `UPDATE notification_deliveries
		SET status = 'failed', error_code = $2, failure_count = failure_count + 1
		WHERE id = $1::uuid`, deliveryID, code)
	return err
}

// deliverTests sends every queued test.
//
// There is no retry. A failed test is a finding rather than a fault to work
// around: the operator wanted to know whether delivery works for this customer
// right now, and "it failed with bot_blocked" is the answer. Retrying would
// turn an answer into a delay.
func (notifier *Notifier) deliverTests(ctx context.Context) {
	tests, err := notifier.store.pendingTests(ctx, notificationBatch)
	if err != nil {
		notifier.logger.Error("test notification lookup failed", "error", err)
		return
	}
	for _, test := range tests {
		if ctx.Err() != nil {
			return
		}
		locale := localeFrom(test.Locale)
		view := testNotificationView(locale, notifier.clock().UTC())
		sendErr := notifier.sender.Send(ctx, test.CustomerID, test.TelegramID, view)
		if sendErr != nil {
			notifier.logger.Warn("test notification delivery failed", "error", sendErr)
		}
		if err := notifier.store.finishTest(ctx, test.DeliveryID, sendErr); err != nil {
			notifier.logger.Error("test notification bookkeeping failed", "error", err)
		}
	}
}

// testNotificationView is the message itself.
//
// It carries the instant it was sent because that is the only thing a customer
// on the phone can read back to support to establish which test they are
// talking about, and it says plainly that an operator sent it — receiving an
// unexplained message from your VPN provider is worse than receiving nothing.
func testNotificationView(locale Locale, at time.Time) View {
	return View{
		Text:    text(locale, "notifications.test", at.Format("2006-01-02 15:04 UTC")),
		Protect: true,
	}
}
