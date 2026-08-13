// Package operator delivers operational notices to a Telegram group and lets an
// operator run backup and restore actions from the bot.
//
// Everything it sends is deliberately content-free: an operator notice names the
// event, the amount class, and identifiers an operator can look up in the admin
// API. It never carries customer text, a subscription link, a token, or a
// payment payload.
package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/platform"
)

// Kinds are the event streams that each get their own forum topic.
var Kinds = []string{"purchase", "renewal", "topup", "refund", "fulfillment_failure", "incident", "backup", "security", "campaign_test"}

// KindCampaignTest is the topic campaign previews go to.
//
// It is a topic rather than a message in the general chat because a marketing
// preview is not an incident, and it is not an operator notification either:
// everything else here is a set of allowlisted, non-personal fields, and this
// one is a rendered message body. Its queue is `campaign_test_sends`, not
// `operator_notifications`, for exactly that reason.
const KindCampaignTest = "campaign_test"

// topicNames are the forum topics the bot creates for the operator. The operator
// supplies only a group; the bot owns every topic in it.
var topicNames = map[string]string{
	"purchase":            "💳 Purchases",
	"renewal":             "♻️ Renewals",
	"topup":               "👛 Wallet top-ups",
	"refund":              "↩️ Refunds",
	"fulfillment_failure": "⚠️ Fulfillment failures",
	"incident":            "🚨 Incidents",
	"backup":              "💾 Backups",
	"security":            "🔐 Admin security",
	"campaign_test":       "📣 Campaign previews",
}

// Config binds the operator group and bounds notification volume.
type Config struct {
	ChatID          int64
	NotificationCap int
	Window          time.Duration
}

// Notifier owns topic binding and delivery.
type Notifier struct {
	pool   *pgxpool.Pool
	client *telegram.Bot
	logger *slog.Logger
	config Config
	clock  func() time.Time
}

func New(pool *pgxpool.Pool, client *telegram.Bot, logger *slog.Logger, config Config) *Notifier {
	if config.Window <= 0 {
		config.Window = 5 * time.Minute
	}
	return &Notifier{pool: pool, client: client, logger: logger, config: config, clock: time.Now}
}

// Configured reports whether an operator group was supplied.
func (notifier *Notifier) Configured() bool {
	return notifier != nil && notifier.config.ChatID != 0 && notifier.client != nil
}

// Run binds every topic and then drains the notification queue.
func (notifier *Notifier) Run(ctx context.Context) {
	if !notifier.Configured() {
		notifier.logger.Info("operator notifications are disabled because no group is configured")
		return
	}
	if err := notifier.EnsureTopics(ctx); err != nil {
		notifier.logger.Error("operator topic binding failed", "error", err)
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		notifier.Dispatch(ctx)
		notifier.DispatchCampaignTests(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// DispatchCampaignTests delivers the campaign previews an operator asked for.
//
// It is a separate pass from Dispatch rather than another kind inside it,
// because the two carry different things. An operator notification is a set of
// allowlisted fields with no content in them; a preview is the campaign's own
// message, rendered exactly as a customer would receive it — which is the only
// version worth reviewing, and the reason the rendering is shared with the
// customer delivery path rather than reimplemented here.
//
// Each row is resolved individually. A preview that fails is recorded as failed
// and not retried: the operator can ask for another one, and a retry loop
// against a chat that is refusing messages would fill the queue rather than the
// chat.
func (notifier *Notifier) DispatchCampaignTests(ctx context.Context) {
	if !notifier.Configured() {
		return
	}
	queries := dbgen.New(notifier.pool)
	pending, err := queries.ListPendingCampaignTestSends(ctx, campaignTestBatch)
	if err != nil {
		notifier.logger.Warn("campaign preview lookup failed", "error", err)
		return
	}
	for _, test := range pending {
		if ctx.Err() != nil {
			return
		}
		status, errorCode := "sent", pgtype.Text{}
		if deliverErr := notifier.deliverCampaignTest(ctx, queries, test); deliverErr != nil {
			status = "failed"
			errorCode = pgtype.Text{String: classifyTopicError(deliverErr), Valid: true}
			notifier.logger.Warn("campaign preview delivery failed",
				"campaign_id", uuidText(test.CampaignID), "code", errorCode.String)
		}
		if err = queries.CompleteCampaignTestSend(ctx, dbgen.CompleteCampaignTestSendParams{
			TestSendID: test.ID, Status: status, ErrorCode: errorCode,
		}); err != nil {
			notifier.logger.Warn("campaign preview bookkeeping failed", "error", err)
			return
		}
	}
}

// campaignTestBatch bounds one pass. Previews are asked for one at a time by a
// person, so this only ever matters after an outage.
const campaignTestBatch = 20

// deliverCampaignTest sends one preview into the campaign topic.
func (notifier *Notifier) deliverCampaignTest(
	ctx context.Context, queries *dbgen.Queries, test dbgen.ListPendingCampaignTestSendsRow,
) error {
	topic, err := queries.GetOperatorTopic(ctx, KindCampaignTest)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !topic.TopicID.Valid {
		if topic, err = notifier.createTopic(ctx, queries, KindCampaignTest); err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}

	subject, body := test.SubjectEn, test.BodyEn
	if test.Locale == "ru" {
		subject, body = test.SubjectRu, test.BodyRu
	}
	_, sendErr := notifier.client.SendMessage(ctx, &telegram.SendMessageParams{
		ChatID:          notifier.config.ChatID,
		MessageThreadID: int(topic.TopicID.Int64),
		Text:            renderCampaignTest(test.CampaignName, test.Locale, subject, body),
		ParseMode:       models.ParseModeHTML,
	})
	return sendErr
}

// renderCampaignTest builds the preview message.
//
// The header names the campaign and the language, because a preview with
// neither is indistinguishable from a campaign that has actually gone out — and
// somebody will eventually see one of these in the group and reach for the
// pause button.
//
// The body is rendered with no variable values, which is what the customer
// delivery path does too: a template variable with nothing behind it becomes an
// empty string there, so a preview that filled in sample values would show copy
// no customer will receive.
func renderCampaignTest(campaign, locale, subject, body string) string {
	builder := &strings.Builder{}
	fmt.Fprintf(builder, "<b>%s</b>\n", html.EscapeString(topicNames[KindCampaignTest]))
	fmt.Fprintf(builder, "campaign: <code>%s</code>\n", html.EscapeString(campaign))
	fmt.Fprintf(builder, "locale: <code>%s</code>\n", html.EscapeString(locale))
	builder.WriteString("This is a preview. Nobody else received it.\n\n")
	if subject != "" {
		fmt.Fprintf(builder, "<b>%s</b>\n\n",
			html.EscapeString(commerce.RenderTemplate(subject, nil)))
	}
	builder.WriteString(html.EscapeString(commerce.RenderTemplate(body, nil)))
	return builder.String()
}

// uuidText renders an identifier for a log line.
func uuidText(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		id.Bytes[0:4], id.Bytes[4:6], id.Bytes[6:8], id.Bytes[8:10], id.Bytes[10:16])
}

// EnsureTopics creates and binds every required forum topic. A topic that was
// deleted in Telegram is recreated on the next send, and a missing permission is
// recorded as an explicit failure state rather than a silent no-op.
func (notifier *Notifier) EnsureTopics(ctx context.Context) error {
	queries := dbgen.New(notifier.pool)
	for _, kind := range Kinds {
		topic, err := queries.UpsertOperatorTopic(ctx, dbgen.UpsertOperatorTopicParams{Kind: kind, ChatID: notifier.config.ChatID})
		if err != nil {
			return err
		}
		if topic.Status == "bound" && topic.TopicID.Valid {
			continue
		}
		if _, err = notifier.createTopic(ctx, queries, kind); err != nil {
			notifier.logger.Warn("operator topic could not be created", "kind", kind, "error", err)
		}
	}
	return nil
}

// createTopic creates one forum topic and records the binding, or records why it
// could not be created.
func (notifier *Notifier) createTopic(ctx context.Context, queries *dbgen.Queries, kind string) (dbgen.OperatorTopic, error) {
	created, err := notifier.client.CreateForumTopic(ctx, &telegram.CreateForumTopicParams{ChatID: notifier.config.ChatID, Name: topicNames[kind]})
	if err != nil {
		code := classifyTopicError(err)
		if _, failErr := queries.FailOperatorTopic(ctx, dbgen.FailOperatorTopicParams{Kind: kind, LastErrorCode: pgtype.Text{String: code, Valid: true}}); failErr != nil {
			return dbgen.OperatorTopic{}, failErr
		}
		return dbgen.OperatorTopic{}, fmt.Errorf("create forum topic: %s", code)
	}
	return queries.BindOperatorTopic(ctx, dbgen.BindOperatorTopicParams{Kind: kind, TopicID: pgtype.Int8{Int64: int64(created.MessageThreadID), Valid: true}})
}

// classifyTopicError reduces a Telegram failure to a stable code an operator can
// act on. The raw API message is dropped because it can echo request content.
func classifyTopicError(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "not enough rights"), strings.Contains(message, "can't manage topics"):
		return "missing_manage_topics_permission"
	case strings.Contains(message, "the group chat was upgraded"), strings.Contains(message, "chat not found"):
		return "chat_not_found"
	case strings.Contains(message, "forum") && strings.Contains(message, "disabled"):
		return "forum_topics_disabled"
	case strings.Contains(message, "bot was kicked"), strings.Contains(message, "bot is not a member"):
		return "bot_not_in_group"
	default:
		return "telegram_error"
	}
}

// Enqueue records an operator notice. The dedupe key makes repeated triggers for
// the same event — a retried webhook, a second reconciliation pass — collapse
// into one message.
func (notifier *Notifier) Enqueue(ctx context.Context, kind, dedupeKey string, payload map[string]any) error {
	if !notifier.Configured() {
		return nil
	}
	encoded, err := json.Marshal(platform.RedactFields(payload))
	if err != nil {
		return err
	}
	_, err = dbgen.New(notifier.pool).EnqueueOperatorNotification(ctx, dbgen.EnqueueOperatorNotificationParams{Kind: kind, DedupeKey: dedupeKey, Payload: encoded})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// Dispatch delivers pending notifications. Delivery is bounded per kind per
// window: past the cap a notice is marked suppressed instead of being sent, so a
// burst of failures cannot flood the operator group.
func (notifier *Notifier) Dispatch(ctx context.Context) {
	if !notifier.Configured() {
		return
	}
	tx, err := notifier.pool.Begin(ctx)
	if err != nil {
		notifier.logger.Warn("operator dispatch could not start", "error", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	pending, err := queries.ListPendingOperatorNotifications(ctx, 50)
	if err != nil {
		notifier.logger.Warn("operator notification lookup failed", "error", err)
		return
	}
	suppressed := map[string]int{}
	for _, notification := range pending {
		sent, countErr := queries.CountRecentOperatorNotifications(ctx, dbgen.CountRecentOperatorNotificationsParams{Kind: notification.Kind, WindowSeconds: notifier.config.Window.Seconds()})
		if countErr != nil {
			notifier.logger.Warn("operator volume check failed", "error", countErr)
			return
		}
		if notifier.config.NotificationCap > 0 && int(sent) >= notifier.config.NotificationCap {
			if _, err = queries.CompleteOperatorNotification(ctx, dbgen.CompleteOperatorNotificationParams{NotificationID: notification.ID, Status: "suppressed", ErrorCode: pgtype.Text{String: "volume_cap", Valid: true}}); err != nil {
				return
			}
			suppressed[notification.Kind]++
			continue
		}
		status, errorCode := "sent", pgtype.Text{}
		if deliverErr := notifier.deliver(ctx, queries, notification); deliverErr != nil {
			status = "failed"
			errorCode = pgtype.Text{String: classifyTopicError(deliverErr), Valid: true}
			notifier.logger.Warn("operator notification delivery failed", "kind", notification.Kind, "code", errorCode.String)
		}
		if _, err = queries.CompleteOperatorNotification(ctx, dbgen.CompleteOperatorNotificationParams{NotificationID: notification.ID, Status: status, ErrorCode: errorCode}); err != nil {
			return
		}
	}
	if err = tx.Commit(ctx); err != nil {
		notifier.logger.Warn("operator dispatch commit failed", "error", err)
		return
	}
	for kind, count := range suppressed {
		notifier.logger.Warn("operator notifications suppressed by the volume cap", "kind", kind, "suppressed", count)
	}
}

// deliver sends one notification into its topic, recreating a topic that was
// deleted in Telegram.
func (notifier *Notifier) deliver(ctx context.Context, queries *dbgen.Queries, notification dbgen.OperatorNotification) error {
	topic, err := queries.GetOperatorTopic(ctx, notification.Kind)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !topic.TopicID.Valid {
		if topic, err = notifier.createTopic(ctx, queries, notification.Kind); err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}
	text := renderNotification(notification)
	_, sendErr := notifier.client.SendMessage(ctx, &telegram.SendMessageParams{
		ChatID:          notifier.config.ChatID,
		MessageThreadID: int(topic.TopicID.Int64),
		Text:            text,
		ParseMode:       models.ParseModeHTML,
	})
	if sendErr == nil {
		return nil
	}
	// A deleted topic is recreated once, then the send is retried.
	if strings.Contains(strings.ToLower(sendErr.Error()), "thread not found") {
		recreated, createErr := notifier.createTopic(ctx, queries, notification.Kind)
		if createErr != nil {
			return createErr
		}
		_, sendErr = notifier.client.SendMessage(ctx, &telegram.SendMessageParams{
			ChatID: notifier.config.ChatID, MessageThreadID: int(recreated.TopicID.Int64),
			Text: text, ParseMode: models.ParseModeHTML,
		})
	}
	return sendErr
}

// renderNotification builds the operator message. Only whitelisted, non-personal
// fields are printed; anything else in the payload is ignored.
func renderNotification(notification dbgen.OperatorNotification) string {
	payload := map[string]any{}
	_ = json.Unmarshal(notification.Payload, &payload)
	builder := &strings.Builder{}
	fmt.Fprintf(builder, "<b>%s</b>\n", html.EscapeString(topicNames[notification.Kind]))
	// Security notices name the event and the operator account it concerns, and
	// nothing else. An operator address, a session token, and a raw client
	// address all stay out of the group chat; they are available in the audit
	// trail to anyone entitled to read it.
	for _, field := range []string{"orderId", "subscriptionId", "entitlementId", "provider", "operation", "currency", "amountMinor", "creditedMinor", "classification", "reason", "source", "errorCode", "fileName", "sizeBytes", "status", "event", "adminUserId", "method"} {
		value, ok := payload[field]
		if !ok || value == nil || value == "" {
			continue
		}
		fmt.Fprintf(builder, "%s: <code>%s</code>\n", field, html.EscapeString(fmt.Sprintf("%v", value)))
	}
	fmt.Fprintf(builder, "at: <code>%s</code>", notification.CreatedAt.Time.UTC().Format(time.RFC3339))
	return builder.String()
}

// MaintenanceChanged posts an incident notice whenever maintenance mode moves.
// It satisfies the maintenance controller's Announcer.
func (notifier *Notifier) MaintenanceChanged(ctx context.Context, state commerce.Maintenance) {
	action := "cleared"
	if state.Active {
		action = "activated"
	}
	key := fmt.Sprintf("maintenance:%s:%d", action, notifier.clock().UTC().Unix()/60)
	if err := notifier.Enqueue(ctx, "incident", key, map[string]any{
		"status": "maintenance_" + action, "source": state.Source, "reason": state.Reason,
	}); err != nil {
		notifier.logger.Warn("maintenance notice could not be queued", "error", err)
	}
}
