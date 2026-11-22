package operator

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// The group's status message.
//
// The startup report is not a new message on every start. Autodeploy restarts
// the bot after nearly every commit, and a fresh report each time would bury
// the infrastructure topic. Instead the bot keeps one status message, pinned in
// that topic, and edits it on every start: the version, what needs attention,
// and the figures are always the latest, and the chat gains nothing. A new
// message is posted (and pinned) only when there is none yet, when it was
// deleted, or when the topics were recreated and the old one is in a topic
// nobody reads.

// SetStartReport hands the notifier the startup report; the next pass posts
// it.
func (notifier *Notifier) SetStartReport(report StartReport) {
	if notifier != nil {
		notifier.startReport.Store(&report)
	}
}

// PostStatus posts or updates the status message once a report is waiting
// and the group is ready for it.
func (notifier *Notifier) PostStatus(ctx context.Context) {
	report := notifier.startReport.Load()
	if report == nil || !notifier.Configured() || notifier.waitingForTelegram() {
		return
	}
	config := notifier.effective(ctx)
	if slices.Contains(config.Disabled, "infrastructure") {
		notifier.startReport.CompareAndSwap(report, nil)
		return
	}
	topicID := int64(0)
	if !config.Flat {
		topic, err := dbgen.New(notifier.pool).GetOperatorTopic(ctx, "infrastructure")
		if err != nil || topic.ChatID != config.ChatID || !topic.TopicID.Valid {
			// The topic is still being made; try on the next pass.
			return
		}
		topicID = topic.TopicID.Int64
	}
	text, markup := renderNotice(renderInput{
		Kind: "infrastructure", Payload: report.decoded(), At: notifier.clock(),
		Locale: config.Locale, Location: config.Location, PanelURL: config.PanelURL,
	})
	var replyMarkup models.ReplyMarkup
	if markup != nil {
		replyMarkup = markup
	}
	if err := notifier.writeStatus(ctx, config.ChatID, topicID, report.Version, text, replyMarkup); err != nil {
		if wait, limited := rateLimited(err); limited {
			notifier.backOff(wait)
			return
		}
		notifier.logger.Warn("operator status message could not be posted", "code", classifyTopicError(err))
	}
	// Posted, or refused for a reason a retry would not fix.
	notifier.startReport.CompareAndSwap(report, nil)
}

// writeStatus edits the stored status message, or posts and pins a new one.
func (notifier *Notifier) writeStatus(
	ctx context.Context, chatID, topicID int64, version, text string, markup models.ReplyMarkup,
) error {
	var messageID, storedTopic int64
	err := notifier.pool.QueryRow(ctx, `SELECT message_id, topic_id FROM operator_status_messages WHERE chat_id = $1`,
		chatID).Scan(&messageID, &storedTopic)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && storedTopic == topicID {
		_, editErr := notifier.client.EditMessageText(ctx, &telegram.EditMessageTextParams{
			ChatID: chatID, MessageID: int(messageID), Text: text,
			ParseMode: models.ParseModeHTML, ReplyMarkup: markup,
		})
		if editErr == nil || strings.Contains(strings.ToLower(editErr.Error()), "message is not modified") {
			_, err = notifier.pool.Exec(ctx, `UPDATE operator_status_messages SET version = $2, updated_at = now()
				WHERE chat_id = $1`, chatID, version)
			return err
		}
		if _, limited := rateLimited(editErr); limited {
			return editErr
		}
		// Deleted, or no longer editable: post a fresh one below.
	}
	sent, err := notifier.client.SendMessage(ctx, &telegram.SendMessageParams{
		ChatID: chatID, MessageThreadID: int(topicID), Text: text,
		ParseMode: models.ParseModeHTML, ReplyMarkup: markup,
	})
	if err != nil {
		return err
	}
	if _, pinErr := notifier.client.PinChatMessage(ctx, &telegram.PinChatMessageParams{
		ChatID: chatID, MessageID: sent.ID, DisableNotification: true,
	}); pinErr != nil {
		// Pinning needs the "Pin messages" right; the message is posted either way.
		notifier.logger.Info("operator status message was not pinned", "code", classifyTopicError(pinErr))
	}
	_, err = notifier.pool.Exec(ctx, `INSERT INTO operator_status_messages (chat_id, topic_id, message_id, version)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (chat_id) DO UPDATE SET topic_id = EXCLUDED.topic_id, message_id = EXCLUDED.message_id,
			version = EXCLUDED.version, updated_at = now()`, chatID, topicID, sent.ID, version)
	return err
}

// decoded is the report's payload as a decoded JSON document, the shape the
// renderer reads every notice payload in.
func (report StartReport) decoded() map[string]any {
	encoded, _ := json.Marshal(report.payload())
	decoded := map[string]any{}
	_ = json.Unmarshal(encoded, &decoded)
	return decoded
}
