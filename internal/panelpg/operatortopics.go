package panelpg

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// OperatorTopic is one forum topic of the operator group: which stream it
// carries, the thread ID the bot saved for it, and whether it exists.
//
// The bot owns the topics. An operator supplies the group in the operator
// group section; the bot creates one topic per notice stream there and saves
// each thread ID in this routing table, so nobody types a thread ID. The panel
// reads the result and can ask for the topics to be made again.
type OperatorTopic struct {
	Kind   string `json:"kind"`
	ChatID int64  `json:"chatId"`
	// TopicID is the forum thread the stream is posted to, once created.
	TopicID *int64 `json:"topicId,omitempty"`
	// Status is "pending" (the bot will create it within a few seconds),
	// "bound", or "failed" with an ErrorCode.
	Status    string    `json:"status"`
	ErrorCode string    `json:"errorCode,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// OperatorTopics lists the operator group's topics.
func (service *Service) OperatorTopics(ctx context.Context) ([]OperatorTopic, error) {
	rows, err := dbgen.New(service.pool).ListOperatorTopics(ctx)
	if err != nil {
		return nil, err
	}
	topics := make([]OperatorTopic, 0, len(rows))
	for _, row := range rows {
		topic := OperatorTopic{
			Kind: row.Kind, ChatID: row.ChatID, Status: row.Status,
			ErrorCode: textValue(row.LastErrorCode), UpdatedAt: timeValue(row.UpdatedAt),
		}
		if row.TopicID.Valid {
			id := row.TopicID.Int64
			topic.TopicID = &id
		}
		topics = append(topics, topic)
	}
	return topics, nil
}

// RecreateOperatorTopics asks the bot to make every topic again: each row
// goes back to pending with no thread, and the bot creates a fresh topic for
// it on its next pass, within about ten seconds, and saves the new thread ID.
// The old topics stay in the group with their history; an operator can close
// or delete them in Telegram.
//
// It is what fixes a group whose topics were deleted by hand, or whose topics
// failed because the bot lacked the "Manage topics" right when it first tried.
func (service *Service) RecreateOperatorTopics(ctx context.Context, actor Actor) (int64, error) {
	var reset int64
	err := service.inTxRaw(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		tag, txErr := tx.Exec(ctx, `UPDATE operator_topics
			SET topic_id = NULL, status = 'pending', last_error_code = NULL, updated_at = now()`)
		if txErr != nil {
			return txErr
		}
		reset = tag.RowsAffected()
		return appendAudit(ctx, queries, actor.audit(
			"panel.operator_topics.recreate_requested", "configuration", "operator_topics", "operator_group",
			map[string]any{"topics": reset},
		))
	})
	return reset, err
}
