// Package operator delivers operational notices to a Telegram group and lets an
// operator run backup and restore actions from the bot.
//
// Everything it sends is deliberately content-free: an operator notice names the
// event, the amounts, the plan, the customer's Telegram handle, and identifiers
// an operator can open in the panel. It never carries customer text, a
// subscription link, a token, or a payment payload.
package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/installsettings"
	"github.com/omniflow/omniflow/internal/platform"
)

// Kinds are the event streams that each get their own forum topic.
var Kinds = []string{"purchase", "renewal", "topup", "refund", "fulfillment_failure", "incident", "backup", "security", "support", "campaign_test", "notice_test", "anomaly", "trial", "addon", "promo", "partner", "error", "infrastructure", "report", "node", "monitoring", "marketing", "receipt"}

// SwitchableKinds are the notification streams an operator may turn off in
// the operator group section. The two preview topics are absent: nothing
// reaches them unless an operator asked for a preview.
var SwitchableKinds = []string{"purchase", "renewal", "topup", "refund", "fulfillment_failure", "incident", "backup", "security", "support", "anomaly", "trial", "addon", "promo", "partner", "error", "infrastructure", "report", "node", "monitoring", "marketing", "receipt"}

// urgentKinds are delivered during quiet hours. Everything else waits, still
// pending, until the window ends.
var urgentKinds = map[string]bool{
	"incident": true, "security": true, "error": true, "fulfillment_failure": true,
	"backup": true, "anomaly": true, "infrastructure": true, "node": true,
}

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
	"support":             "💬 Support tickets",
	"campaign_test":       "📣 Campaign previews",
	"notice_test":         "🔔 Notice previews",
	"node":                "🖥 Remnawave nodes",
	"monitoring":          "📈 Usage monitoring",
	"anomaly":             "📈 Anomalies",
	"trial":               "🎁 Trials",
	"addon":               "➕ Add-ons",
	"promo":               "🏷 Promo codes",
	"partner":             "🤝 Partners",
	"error":               "🐞 Errors",
	"infrastructure":      "🛠 Infrastructure",
	"report":              "📊 Reports",
	"marketing":           "🎯 Loyalty and referrals",
	"receipt":             "🧾 Self-employed receipts",
}

// Config binds the operator group and bounds notification volume.
type Config struct {
	ChatID          int64
	NotificationCap int
	Window          time.Duration
	// PanelURL is APP_PUBLIC_URL. A sale notice links the order and the buyer
	// in the panel when it is an HTTPS address.
	PanelURL string
	// Flat sends every notice to the group itself instead of a topic per
	// kind, for a group without topics.
	Flat bool
	// Disabled names the kinds an operator switched off. Their notices are
	// completed as suppressed with error code `disabled`.
	Disabled []string
	// Held names the kinds kept pending right now by quiet hours.
	Held []string
	// MutedSupport names support notice events switched off in the support
	// section: "new" (a new ticket) and "customer_reply".
	MutedSupport []string
	// Locale is the language notices are written in: "en" or "ru".
	Locale string
	// Location is the installation's timezone, which notice times are in.
	Location *time.Location
}

// Notifier owns topic binding and delivery.
type Notifier struct {
	pool     *pgxpool.Pool
	client   *telegram.Bot
	logger   *slog.Logger
	config   Config
	clock    func() time.Time
	settings *installsettings.Reader
}

// WithSettings lets the operator group and notifications sections of the
// installation settings override the environment on every pass: the chat,
// topics or a flat chat, the volume cap and its window, the kinds switched
// off, and quiet hours.
func (notifier *Notifier) WithSettings(reader *installsettings.Reader) *Notifier {
	notifier.settings = reader
	return notifier
}

// effective is the configuration for one pass.
func (notifier *Notifier) effective(ctx context.Context) Config {
	config := notifier.config
	if notifier.settings == nil {
		return config
	}
	group := notifier.settings.Section(ctx, "operator_group")
	if raw := group.String("chatId", ""); raw != "" {
		if chat, err := strconv.ParseInt(raw, 10, 64); err == nil && chat != 0 {
			config.ChatID = chat
		}
	}
	config.Flat = !group.Bool("createTopics", !config.Flat)
	if locale := group.String("locale", config.Locale); locale == "ru" || locale == "en" {
		config.Locale = locale
	}
	config.Location = notifier.settings.Location(ctx)
	if group.Has("notificationCap") {
		config.NotificationCap = max(group.Int("notificationCap", config.NotificationCap), 0)
	}
	config.Window = group.Duration("windowMinutes", time.Minute, config.Window)
	config.Disabled = group.Strings("disabledKinds", config.Disabled)
	config.Held = HeldKinds(notifier.clock().In(notifier.settings.Location(ctx)),
		notifier.settings.Section(ctx, "notifications"))
	support := notifier.settings.Section(ctx, "support")
	config.MutedSupport = nil
	if !support.Bool("notifyNewTicket", true) {
		config.MutedSupport = append(config.MutedSupport, "new")
	}
	if !support.Bool("notifyCustomerReply", true) {
		config.MutedSupport = append(config.MutedSupport, "customer_reply")
	}
	return config
}

// supportEvent names a support notice's event; a new ticket carries none.
func supportEvent(notification dbgen.OperatorNotification) string {
	payload := struct {
		Event string `json:"event"`
	}{}
	_ = json.Unmarshal(notification.Payload, &payload)
	if payload.Event == "" {
		return "new"
	}
	return payload.Event
}

// HeldKinds lists the kinds quiet hours keep pending at a local time.
func HeldKinds(local time.Time, quiet installsettings.Document) []string {
	if !installsettings.WithinWindow(local, quiet.String("quietHoursStart", ""), quiet.String("quietHoursEnd", "")) {
		return nil
	}
	var held []string
	for _, kind := range Kinds {
		if !urgentKinds[kind] {
			held = append(held, kind)
		}
	}
	return held
}

func New(pool *pgxpool.Pool, client *telegram.Bot, logger *slog.Logger, config Config) *Notifier {
	if config.Window <= 0 {
		config.Window = 5 * time.Minute
	}
	return &Notifier{pool: pool, client: client, logger: logger, config: config, clock: time.Now}
}

// Configured reports whether an operator group was supplied, in the
// environment or in the panel.
func (notifier *Notifier) Configured() bool {
	return notifier != nil && notifier.client != nil &&
		notifier.effective(context.Background()).ChatID != 0
}

// Run binds every topic and then drains the notification queue. A group
// saved in the panel later is picked up on the next pass, so the loop runs
// even when no group is configured yet.
func (notifier *Notifier) Run(ctx context.Context) {
	if notifier == nil || notifier.client == nil {
		return
	}
	bound := int64(0)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		config := notifier.effective(ctx)
		switch {
		case config.ChatID != 0 && config.ChatID != bound && !config.Flat:
			if err := notifier.EnsureTopics(ctx); err != nil {
				notifier.logger.Error("operator topic binding failed", "error", err)
			}
			bound = config.ChatID
		case config.ChatID == 0 && bound == 0:
			bound = -1
			notifier.logger.Info("operator notifications are waiting for a group to be configured")
		}
		notifier.Dispatch(ctx)
		notifier.DispatchCampaignTests(ctx)
		notifier.DispatchNoticeTests(ctx)
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
		ChatID:          notifier.effective(ctx).ChatID,
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
	chat := notifier.effective(ctx).ChatID
	for _, kind := range Kinds {
		topic, err := queries.UpsertOperatorTopic(ctx, dbgen.UpsertOperatorTopicParams{Kind: kind, ChatID: chat})
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
	created, err := notifier.client.CreateForumTopic(ctx, &telegram.CreateForumTopicParams{ChatID: notifier.effective(ctx).ChatID, Name: topicNames[kind]})
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
	config := notifier.effective(ctx)
	if config.ChatID == 0 {
		return
	}
	held := config.Held
	if held == nil {
		held = []string{}
	}
	pending, err := queries.ListDueOperatorNotifications(ctx, dbgen.ListDueOperatorNotificationsParams{HeldKinds: held, PageSize: 50})
	if err != nil {
		notifier.logger.Warn("operator notification lookup failed", "error", err)
		return
	}
	suppressed := map[string]int{}
	for _, notification := range pending {
		if slices.Contains(config.Disabled, notification.Kind) ||
			notification.Kind == "support" && slices.Contains(config.MutedSupport, supportEvent(notification)) {
			if _, err = queries.CompleteOperatorNotification(ctx, dbgen.CompleteOperatorNotificationParams{NotificationID: notification.ID, Status: "suppressed", ErrorCode: pgtype.Text{String: "disabled", Valid: true}}); err != nil {
				return
			}
			continue
		}
		sent, countErr := queries.CountRecentOperatorNotifications(ctx, dbgen.CountRecentOperatorNotificationsParams{Kind: notification.Kind, WindowSeconds: config.Window.Seconds()})
		if countErr != nil {
			notifier.logger.Warn("operator volume check failed", "error", countErr)
			return
		}
		if config.NotificationCap > 0 && int(sent) >= config.NotificationCap {
			if _, err = queries.CompleteOperatorNotification(ctx, dbgen.CompleteOperatorNotificationParams{NotificationID: notification.ID, Status: "suppressed", ErrorCode: pgtype.Text{String: "volume_cap", Valid: true}}); err != nil {
				return
			}
			suppressed[notification.Kind]++
			continue
		}
		status, errorCode := "sent", pgtype.Text{}
		if deliverErr := notifier.deliver(ctx, queries, config, notification); deliverErr != nil {
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
func (notifier *Notifier) deliver(ctx context.Context, queries *dbgen.Queries, config Config, notification dbgen.OperatorNotification) error {
	topic := dbgen.OperatorTopic{}
	if !config.Flat {
		var err error
		topic, err = queries.GetOperatorTopic(ctx, notification.Kind)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !topic.TopicID.Valid {
			if topic, err = notifier.createTopic(ctx, queries, notification.Kind); err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
	}
	var (
		text   string
		markup models.ReplyMarkup
	)
	if sale, ok := decodeGoodsSale(notification); ok {
		text = renderGoodsSale(sale, notification.CreatedAt.Time)
		if keyboard := goodsSaleMarkup(sale, notifier.config.PanelURL); keyboard != nil {
			markup = keyboard
		}
	} else {
		text, markup = notifier.render(ctx, config, notification)
	}
	_, sendErr := notifier.client.SendMessage(ctx, &telegram.SendMessageParams{
		ChatID:          config.ChatID,
		MessageThreadID: int(topic.TopicID.Int64),
		Text:            text,
		ParseMode:       models.ParseModeHTML,
		ReplyMarkup:     markup,
	})
	if sendErr == nil {
		return nil
	}
	// A deleted topic is recreated once, then the send is retried.
	if !config.Flat && strings.Contains(strings.ToLower(sendErr.Error()), "thread not found") {
		recreated, createErr := notifier.createTopic(ctx, queries, notification.Kind)
		if createErr != nil {
			return createErr
		}
		_, sendErr = notifier.client.SendMessage(ctx, &telegram.SendMessageParams{
			ChatID: config.ChatID, MessageThreadID: int(recreated.TopicID.Int64),
			Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: markup,
		})
	}
	return sendErr
}

// render builds the operator message: the shop sale and the scheduled report
// by their own renderers, everything else by renderNotice from the payload and
// what the database says about the order, customer, and ticket it names.
//
// The payload is still the allowlist it always was. Security notices name the
// event and the operator account it concerns, and nothing else: an operator
// address, a session token, and a raw client address stay out of the group
// chat; they are in the audit trail for anyone entitled to read it.
func (notifier *Notifier) render(ctx context.Context, config Config, notification dbgen.OperatorNotification) (string, models.ReplyMarkup) {
	payload := map[string]any{}
	_ = json.Unmarshal(notification.Payload, &payload)
	if notification.Kind == "report" {
		return renderReport(notification, payload), nil
	}
	text, markup := RenderNotice(ctx, notifier.pool, notification.Kind, payload, notification.CreatedAt.Time, config)
	if markup == nil {
		return text, nil
	}
	return text, markup
}

// RenderNotice is the message a notice of a kind and payload becomes: what
// the dispatcher sends, with the order, customer, and ticket it names read
// from the database. config supplies the language, the timezone, and the
// panel address the buttons link to.
func RenderNotice(
	ctx context.Context, pool *pgxpool.Pool, kind string, payload map[string]any, at time.Time, config Config,
) (string, *models.InlineKeyboardMarkup) {
	return renderNotice(renderInput{
		Kind: kind, Payload: payload, Facts: lookupFacts(ctx, pool, payload, config.Locale),
		At: at, Locale: config.Locale, Location: config.Location, PanelURL: config.PanelURL,
	})
}

// notificationFields is the allowlist a notice is rendered from.
var notificationFields = []string{
	"orderId", "subscriptionId", "entitlementId", "provider", "operation", "currency",
	"amountMinor", "creditedMinor", "classification", "reason", "source", "errorCode",
	"fileName", "sizeBytes", "status", "event", "adminUserId", "method", "ticketId", "queueId",
	// Anomaly signals: the metric and its numbers, never the subject.
	"signalId", "metric", "severity", "observed", "threshold", "subjectType",
	// Remnawave nodes and usage monitoring.
	"node", "country", "remnawaveUserId", "customerId", "users", "devices", "limit", "gigabytes", "window",
	// Errors and infrastructure: where, which, how often, and which build.
	"process", "fingerprint", "occurrences", "version", "previousVersion", "releaseUrl",
	// Support SLA breaches.
	"overdueMinutes", "priority",
	// Loyalty tier changes.
	"fromTier", "toTier",
	// Self-employed receipts.
	"receiptUrl",
	// A previous bot's payment that settled after the migration: the
	// provider's own payment ID, to look it up in the provider's dashboard.
	"providerReference",
}

// MaintenanceChanged posts an incident notice whenever maintenance mode moves.
// It satisfies the maintenance controller's Announcer.
func (notifier *Notifier) MaintenanceChanged(ctx context.Context, state commerce.Maintenance) {
	key, payload := maintenanceNotice(state, notifier.clock())
	if err := notifier.Enqueue(ctx, "incident", key, payload); err != nil {
		notifier.logger.Warn("maintenance notice could not be queued", "error", err)
	}
}

// QueuedAnnouncer tells operators about maintenance transitions from a process
// that has no Telegram client. Maintenance is detected in the api process while
// the notifier that talks to the group runs in the bot, so the api writes the
// notice into the same queue the bot drains; the dedupe key matches the one
// the bot-side announcer would use, so one transition is one message.
type QueuedAnnouncer struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	clock  func() time.Time
}

// NewQueuedAnnouncer builds an announcer that queues notices in PostgreSQL.
func NewQueuedAnnouncer(pool *pgxpool.Pool, logger *slog.Logger) *QueuedAnnouncer {
	return &QueuedAnnouncer{pool: pool, logger: logger, clock: time.Now}
}

// MaintenanceChanged queues an incident notice for a maintenance transition.
func (announcer *QueuedAnnouncer) MaintenanceChanged(ctx context.Context, state commerce.Maintenance) {
	key, payload := maintenanceNotice(state, announcer.clock())
	encoded, err := json.Marshal(platform.RedactFields(payload))
	if err == nil {
		_, err = dbgen.New(announcer.pool).EnqueueOperatorNotification(ctx, dbgen.EnqueueOperatorNotificationParams{
			Kind: "incident", DedupeKey: key, Payload: encoded,
		})
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		announcer.logger.Warn("maintenance notice could not be queued", "error", err)
	}
}

// maintenanceNotice is the dedupe key and payload of a maintenance incident,
// shared by both announcers.
func maintenanceNotice(state commerce.Maintenance, now time.Time) (string, map[string]any) {
	action := "cleared"
	if state.Active {
		action = "activated"
	}
	key := fmt.Sprintf("maintenance:%s:%d", action, now.UTC().Unix()/60)
	return key, map[string]any{
		"status": "maintenance_" + action, "source": state.Source, "reason": state.Reason,
	}
}
