package operator

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// KindNoticeTest is the topic transactional-notice previews go to.
//
// It is a separate topic from campaign previews for the same reason campaign
// previews are separate from notifications: the group is read by people looking
// for something specific, and a marketing draft and a rewritten expiry warning
// are not the same errand. Both are rendered message bodies rather than
// allowlisted fields, which is why neither belongs in the notification stream.
const KindNoticeTest = "notice_test"

// noticeTestBatch bounds one pass. Previews are asked for one at a time by a
// person, so this only matters after an outage.
const noticeTestBatch = 20

// DispatchNoticeTests delivers the transactional-notice previews an operator
// asked for.
//
// The body arrives already rendered. The panel resolved it at the moment the
// operator pressed the button, against the sample values from the notice
// definition, so the group sees the text that was in the editor rather than
// whatever is saved by the time this runs — which may be different, and would
// make the preview answer a question nobody asked.
//
// A failed preview is recorded as failed and not retried. The operator can ask
// for another; a retry loop against a chat that is refusing messages would fill
// the queue rather than the chat.
func (notifier *Notifier) DispatchNoticeTests(ctx context.Context) {
	if !notifier.Configured() {
		return
	}
	queries := dbgen.New(notifier.pool)
	pending, err := queries.ListPendingNoticeTestSends(ctx, noticeTestBatch)
	if err != nil {
		notifier.logger.Warn("notice preview lookup failed", "error", err)
		return
	}
	for _, test := range pending {
		if ctx.Err() != nil {
			return
		}
		status, errorCode := "sent", pgtype.Text{}
		if deliverErr := notifier.deliverNoticeTest(ctx, queries, test); deliverErr != nil {
			status = "failed"
			errorCode = pgtype.Text{String: classifyTopicError(deliverErr), Valid: true}
			notifier.logger.Warn("notice preview delivery failed",
				"code", test.Code, "reason", errorCode.String)
		}
		if err = queries.CompleteNoticeTestSend(ctx, dbgen.CompleteNoticeTestSendParams{
			TestSendID: test.ID, Status: status, ErrorCode: errorCode,
		}); err != nil {
			notifier.logger.Warn("notice preview bookkeeping failed", "error", err)
			return
		}
	}
}

func (notifier *Notifier) deliverNoticeTest(
	ctx context.Context, queries *dbgen.Queries, test dbgen.ListPendingNoticeTestSendsRow,
) error {
	topic, err := queries.GetOperatorTopic(ctx, KindNoticeTest)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !topic.TopicID.Valid {
		if topic, err = notifier.createTopic(ctx, queries, KindNoticeTest); err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}

	_, sendErr := notifier.client.SendMessage(ctx, &telegram.SendMessageParams{
		ChatID:          notifier.config.ChatID,
		MessageThreadID: int(topic.TopicID.Int64),
		Text:            renderNoticeTest(test.Code, test.Locale, test.Body),
		ParseMode:       models.ParseModeHTML,
	})
	return sendErr
}

// renderNoticeTest builds the preview message.
//
// The header names the notice and the language and says plainly that nobody
// received it. Without that, somebody reading the group sees an expiry warning
// and concludes the installation just sent one to a customer — and the whole
// value of a preview is lost the moment it is mistaken for the real thing.
//
// The body is sent as-is. It was validated when it was written and rendered
// when it was requested, and escaping it here would show the operator their own
// markup instead of the message it produces.
func renderNoticeTest(code, locale, body string) string {
	builder := &strings.Builder{}
	fmt.Fprintf(builder, "<b>%s</b>\n", html.EscapeString(topicNames[KindNoticeTest]))
	fmt.Fprintf(builder, "notice: <code>%s</code>\n", html.EscapeString(code))
	fmt.Fprintf(builder, "locale: <code>%s</code>\n", html.EscapeString(locale))
	builder.WriteString("This is a preview with sample values. No customer received it.\n\n")
	builder.WriteString(body)
	return builder.String()
}
