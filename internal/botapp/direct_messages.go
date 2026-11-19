package botapp

import (
	"context"
	"errors"
	"html"
)

type pendingDirectMessage struct {
	ID         string
	CustomerID string
	TelegramID int64
	Body       string
}

// pendingDirectMessages lists operator messages waiting to be delivered to a
// customer who can be reached in Telegram.
func (store *PostgresStore) pendingDirectMessages(ctx context.Context, limit int) ([]pendingDirectMessage, error) {
	rows, err := store.pool.Query(ctx, telegramRecipients+`
		SELECT DISTINCT ON (m.id) m.id::text, m.user_id::text, recipient.telegram_id, m.body
		FROM customer_direct_messages m
		JOIN users u ON u.id = m.user_id
		JOIN recipient ON recipient.user_id = m.user_id
		WHERE m.status = 'pending' AND u.status = 'active'
		ORDER BY m.id, m.created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]pendingDirectMessage, 0, limit)
	for rows.Next() {
		var message pendingDirectMessage
		if err := rows.Scan(&message.ID, &message.CustomerID, &message.TelegramID, &message.Body); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (store *PostgresStore) finishDirectMessage(ctx context.Context, id string, sendErr error) error {
	if sendErr == nil {
		_, err := store.pool.Exec(ctx, `UPDATE customer_direct_messages SET status = 'sent', sent_at = now(), error_code = NULL WHERE id = $1::uuid`, id)
		return err
	}
	code := "telegram_unavailable"
	var classified *DeliveryError
	if errors.As(sendErr, &classified) {
		code = classified.Code
	}
	_, err := store.pool.Exec(ctx, `UPDATE customer_direct_messages SET status = 'failed', error_code = $2 WHERE id = $1::uuid`, id, code)
	return err
}

// deliverDirectMessages sends what operators wrote to customers from the
// panel. The text is the operator's and is sent as-is, escaped, so nothing in
// it can be read as markup.
func (notifier *Notifier) deliverDirectMessages(ctx context.Context) {
	messages, err := notifier.store.pendingDirectMessages(ctx, notificationBatch)
	if err != nil {
		notifier.logger.Error("direct message lookup failed", "error", err)
		return
	}
	for _, message := range messages {
		if ctx.Err() != nil {
			return
		}
		sendErr := notifier.sender.Send(ctx, message.CustomerID, message.TelegramID, View{Text: html.EscapeString(message.Body)})
		if sendErr != nil {
			notifier.logger.Warn("direct message delivery failed", "error", sendErr)
		}
		if err := notifier.store.finishDirectMessage(ctx, message.ID, sendErr); err != nil {
			notifier.logger.Error("direct message bookkeeping failed", "error", err)
		}
	}
}
