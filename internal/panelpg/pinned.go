package panelpg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/notice"
)

// PinnedMessage is the operator's message for every customer.
type PinnedMessage struct {
	ID          string     `json:"id"`
	TextRU      string     `json:"textRu"`
	TextEN      string     `json:"textEn"`
	PhotoURL    string     `json:"photoUrl,omitempty"`
	Pin         bool       `json:"pin"`
	ShowOnStart bool       `json:"showOnStart"`
	Status      string     `json:"status"`
	Sent        int64      `json:"sent"`
	Failed      int64      `json:"failed"`
	CreatedAt   time.Time  `json:"createdAt"`
	RetiredAt   *time.Time `json:"retiredAt,omitempty"`
}

// PinnedMessages lists the latest messages with their reach.
func (service *Service) PinnedMessages(ctx context.Context) ([]PinnedMessage, error) {
	rows, err := service.pool.Query(ctx, `SELECT p.id::text, p.text_ru, p.text_en, COALESCE(p.photo_url, ''), p.pin,
			p.show_on_start, p.status, p.created_at, p.retired_at,
			count(d.user_id) FILTER (WHERE d.status = 'sent'), count(d.user_id) FILTER (WHERE d.status = 'failed')
		FROM pinned_messages p LEFT JOIN pinned_message_deliveries d ON d.pinned_id = p.id
		GROUP BY p.id ORDER BY p.created_at DESC LIMIT 10`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []PinnedMessage{}
	for rows.Next() {
		var message PinnedMessage
		if err := rows.Scan(&message.ID, &message.TextRU, &message.TextEN, &message.PhotoURL, &message.Pin,
			&message.ShowOnStart, &message.Status, &message.CreatedAt, &message.RetiredAt,
			&message.Sent, &message.Failed); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

// PublishPinned retires the message in force and publishes a new one; the bot
// unpins the old one and sends the new one to everybody.
func (service *Service) PublishPinned(ctx context.Context, message PinnedMessage, actor Actor) (PinnedMessage, error) {
	message.PhotoURL = strings.TrimSpace(message.PhotoURL)
	if strings.TrimSpace(message.TextRU) == "" || strings.TrimSpace(message.TextEN) == "" ||
		message.PhotoURL != "" && (!strings.HasPrefix(message.PhotoURL, "https://") || len(message.PhotoURL) > 500) {
		return PinnedMessage{}, ErrValidaton
	}
	for _, body := range []string{message.TextRU, message.TextEN} {
		if err := notice.CheckMarkup(body); err != nil {
			return PinnedMessage{}, fmt.Errorf("%w: %w", ErrValidaton, err)
		}
	}
	err := service.inTxRaw(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		if _, err := tx.Exec(ctx, `UPDATE pinned_messages SET status = 'retired', retired_at = now() WHERE status = 'active'`); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO pinned_messages (text_ru, text_en, photo_url, pin, show_on_start, created_by)
			VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6) RETURNING id::text, status, created_at`,
			message.TextRU, message.TextEN, message.PhotoURL, message.Pin, message.ShowOnStart,
			optionalUUID(actor.AdminID)).Scan(&message.ID, &message.Status, &message.CreatedAt); err != nil {
			return rejected(err)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.pinned_message.published", "marketing", "pinned_message", message.ID,
			map[string]any{"pin": message.Pin, "showOnStart": message.ShowOnStart},
		))
	})
	return message, err
}

// RetirePinned stops a message; the bot unpins it everywhere it pinned it.
func (service *Service) RetirePinned(ctx context.Context, id string, actor Actor) error {
	parsed, err := parseUUID(id)
	if err != nil {
		return err
	}
	return service.inTxRaw(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		result, err := tx.Exec(ctx, `UPDATE pinned_messages SET status = 'retired', retired_at = now()
			WHERE id = $1 AND status = 'active'`, parsed)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit("panel.pinned_message.retired", "marketing", "pinned_message", id, nil))
	})
}
