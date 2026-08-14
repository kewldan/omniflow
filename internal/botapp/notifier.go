package botapp

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	"github.com/omniflow/omniflow/internal/remnawave"
)

const notificationInterval = 15 * time.Minute

// notificationBatch bounds one pass so a large installation degrades in
// throughput rather than in latency for everyone.
const notificationBatch = 200

type notificationCandidate struct {
	UserID               string
	TelegramID           int64
	RemnawaveID          int64
	Locale               string
	ExpiryNotifications  bool
	TrafficNotifications bool
	RenewalNotifications bool
	NewsNotifications    bool
	MarketingEnabled     bool
	QuietHoursStart      pgtype.Int2
	QuietHoursEnd        pgtype.Int2
	Timezone             string
	DeliveryStatus       string
	RetryAfter           pgtype.Timestamptz
	MarketingSent        int
}

func (candidate notificationCandidate) quietHours() commerce.QuietHours {
	if !candidate.QuietHoursStart.Valid || !candidate.QuietHoursEnd.Valid {
		return commerce.QuietHours{}
	}
	return commerce.QuietHours{Configured: true, StartHour: int(candidate.QuietHoursStart.Int16), EndHour: int(candidate.QuietHoursEnd.Int16), Location: loadLocation(candidate.Timezone)}
}

// kindEnabled maps a notification kind onto the customer's preference. Payment,
// fulfillment, support, and referral messages are consequences of the customer's
// own actions and have no opt-out.
func kindEnabled(kind string, candidate notificationCandidate) bool {
	switch kind {
	case "expiry":
		return candidate.ExpiryNotifications
	case "traffic":
		return candidate.TrafficNotifications
	case "renewal", "grace", "recovery", "trial":
		return candidate.RenewalNotifications
	case "news", "announcement", "incident", "maintenance":
		return candidate.NewsNotifications
	case "marketing":
		return candidate.MarketingEnabled
	default:
		return true
	}
}

// Notifier delivers every customer notification the bot owns, applying consent,
// per-kind preferences, quiet hours, and marketing frequency caps before any
// message reaches Telegram.
type Notifier struct {
	logger    *slog.Logger
	sender    *Sender
	store     *PostgresStore
	remnawave remnawave.Service
	commerce  *Commerce
	settings  CommerceSettings
	clock     func() time.Time
	// notices is the operator wording in force for the current pass. The zero
	// value means "shipped defaults", so a notifier that has never refreshed —
	// or one whose refresh failed — still sends every notice.
	notices notices
}

// refreshNotices reloads the operator wording for this pass.
//
// A failure keeps whatever was in force rather than clearing it. Falling back
// to the shipped defaults on a transient database error would change what every
// customer reads because one query timed out, and the previous pass's wording
// is far closer to correct than that.
func (notifier *Notifier) refreshNotices(ctx context.Context) {
	loaded, err := notifier.store.loadNotices(ctx)
	if err != nil {
		notifier.logger.Warn("notice override lookup failed", "error", err)
		return
	}
	notifier.notices = loaded
}

// NewNotifier builds the notification pipeline. A nil commerce service disables
// the commerce-dependent notifications and leaves the v0.2 alerts running.
func NewNotifier(logger *slog.Logger, sender *Sender, store *PostgresStore, service remnawave.Service, commerceService *Commerce, settings CommerceSettings) *Notifier {
	return &Notifier{logger: logger, sender: sender, store: store, remnawave: service, commerce: commerceService, settings: settings, clock: time.Now}
}

// Run delivers notifications until the context is cancelled.
func (notifier *Notifier) Run(ctx context.Context) {
	notifier.RunOnce(ctx)
	ticker := time.NewTicker(notificationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			notifier.RunOnce(ctx)
		}
	}
}

// RunOnce executes one full notification pass.
func (notifier *Notifier) RunOnce(ctx context.Context) {
	// The operator's wording is read once and used for every message in this
	// pass. A save halfway through a batch takes effect on the next one, which
	// is both cheaper than a query per message and more coherent: a hundred
	// expiry warnings sent together say the same thing.
	notifier.refreshNotices(ctx)
	notifier.deliverSupportReplies(ctx)
	notifier.deliverTests(ctx)
	notifier.deliverDunning(ctx)
	notifier.deliverCampaigns(ctx)
	candidates, err := notifier.store.notificationCandidates(ctx, notifier.settings.MarketingWindow)
	if err != nil {
		notifier.logger.Error("notification candidate lookup failed", "error", err)
		return
	}
	notifier.deliverNews(ctx, candidates)
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return
		}
		notifier.deliverLifecycle(ctx, candidate)
	}
}

// deliverLifecycle walks every subscription a customer owns. Alerts are keyed
// per subscription, so a busy subscription can never suppress a quiet one's
// expiry or traffic notice, and every message names the subscription it is
// about as soon as the customer holds more than one.
func (notifier *Notifier) deliverLifecycle(ctx context.Context, candidate notificationCandidate) {
	locale := localeFrom(candidate.Locale)
	subscriptions, err := notifier.store.Subscriptions(ctx, candidate.UserID, locale)
	if err != nil {
		notifier.logger.Warn("subscription lookup failed for notifications", "error", err)
		return
	}
	if len(subscriptions) == 0 {
		// A customer imported before v0.5 may still have only the
		// customer-level Remnawave mapping. Alerts keep working for them.
		notifier.deliverSubscriptionAlerts(ctx, candidate, SubscriptionSummary{RemnawaveID: candidate.RemnawaveID}, locale, false)
		return
	}
	named := len(subscriptions) > 1
	for _, subscription := range subscriptions {
		if ctx.Err() != nil {
			return
		}
		notifier.deliverSubscriptionAlerts(ctx, candidate, subscription, locale, named)
	}
}

func (notifier *Notifier) deliverSubscriptionAlerts(ctx context.Context, candidate notificationCandidate, subscription SubscriptionSummary, locale Locale, named bool) {
	if subscription.RemnawaveID > 0 {
		user, lookupErr := notifier.remnawave.User(ctx, subscription.RemnawaveID)
		if lookupErr != nil {
			notifier.logger.Warn("notification user lookup failed", "error", lookupErr)
		} else {
			notifier.maybeSendExpiry(ctx, candidate, subscription, user, locale, named)
			notifier.maybeSendTraffic(ctx, candidate, subscription, user, locale, named)
		}
	}
	if notifier.commerce == nil {
		return
	}
	entitlement, err := notifier.store.EntitlementForSubscription(ctx, candidate.UserID, subscription.ID, locale, notifier.settings.Currency)
	if err != nil {
		notifier.logger.Warn("entitlement lookup failed for notifications", "error", err)
		return
	}
	if !entitlement.Found {
		return
	}
	if named {
		entitlement.SubscriptionLabel = subscription.Label
	}
	now := notifier.clock().UTC()
	if offset, due := commerce.RenewalReminderDue(now, entitlement.EndsAt); due {
		dedupe := fmt.Sprintf("%s:%d", entitlement.ID, offset)
		notifier.deliver(ctx, candidate, subscription.ID, "renewal", dedupe, renewalReminderView(notifier.notices, locale, entitlement, offset))
	}
	if entitlement.GracePeriod > 0 && !now.Before(entitlement.EndsAt) && now.Before(entitlement.EndsAt.Add(entitlement.GracePeriod)) {
		notifier.deliver(ctx, candidate, subscription.ID, "grace", entitlement.ID, gracePeriodView(notifier.notices, locale, entitlement))
	}
	if commerce.RecoveryDue(now, entitlement.EndsAt, entitlement.GracePeriod, notifier.settings.RecoveryWindow) {
		notifier.deliver(ctx, candidate, subscription.ID, "recovery", entitlement.ID, recoveryView(notifier.notices, locale, entitlement))
	}
	notifier.deliverFulfillment(ctx, candidate, subscription.ID, entitlement, locale)
}

// deliverFulfillment tells a customer when provisioning finished, or when it is
// taking long enough that they deserve an explanation. The payment is never at
// risk in either case.
func (notifier *Notifier) deliverFulfillment(ctx context.Context, candidate notificationCandidate, subscriptionID string, entitlement Entitlement, locale Locale) {
	status, err := notifier.store.LatestFulfillmentStatus(ctx, entitlement.ID)
	if err != nil {
		notifier.logger.Warn("fulfillment status lookup failed", "error", err)
		return
	}
	switch status {
	case "succeeded":
		notifier.deliver(ctx, candidate, subscriptionID, "fulfillment", entitlement.ID+":succeeded", fulfillmentAlertView(notifier.notices, locale, true, entitlement.SubscriptionLabel))
	case "failed":
		notifier.deliver(ctx, candidate, subscriptionID, "fulfillment", entitlement.ID+":failed", fulfillmentAlertView(notifier.notices, locale, false, entitlement.SubscriptionLabel))
	}
}

func (notifier *Notifier) maybeSendExpiry(ctx context.Context, candidate notificationCandidate, subscription SubscriptionSummary, user remnawave.User, locale Locale, named bool) {
	days := int(math.Ceil(time.Until(user.ExpireAt).Hours() / 24))
	if days < 0 {
		days = 0
	}
	if days != 7 && days != 3 && days != 1 && days != 0 {
		return
	}
	dedupe := fmt.Sprintf("%s:%d", user.ExpireAt.UTC().Format(time.DateOnly), days)
	notifier.deliver(ctx, candidate, subscription.ID, "expiry", dedupe, expiryAlertView(notifier.notices, locale, days, subscriptionLabelFor(subscription, named)))
}

func (notifier *Notifier) maybeSendTraffic(ctx context.Context, candidate notificationCandidate, subscription SubscriptionSummary, user remnawave.User, locale Locale, named bool) {
	if user.TrafficLimitBytes <= 0 {
		return
	}
	percent := int64(math.Floor(float64(user.Traffic.UsedBytes) / float64(user.TrafficLimitBytes) * 100))
	threshold := int64(0)
	if percent >= 100 {
		threshold = 100
	} else if percent >= 80 {
		threshold = 80
	}
	if threshold == 0 {
		return
	}
	dedupe := fmt.Sprintf("%d:%d:%s", threshold, user.TrafficLimitBytes, user.ExpireAt.UTC().Format(time.DateOnly))
	notifier.deliver(ctx, candidate, subscription.ID, "traffic", dedupe, trafficAlertView(notifier.notices, locale, percent, subscriptionLabelFor(subscription, named)))
}

// subscriptionLabelFor returns the label an alert should carry, or an empty
// string when the customer holds exactly one subscription and naming it would
// only add noise.
func subscriptionLabelFor(subscription SubscriptionSummary, named bool) string {
	if !named {
		return ""
	}
	return subscription.Label
}

func (notifier *Notifier) deliverSupportReplies(ctx context.Context) {
	replies, err := notifier.store.PendingOperatorReplies(ctx, notificationBatch)
	if err != nil {
		notifier.logger.Error("operator reply lookup failed", "error", err)
		return
	}
	for _, reply := range replies {
		if ctx.Err() != nil {
			return
		}
		locale := localeFrom(reply.Locale)
		view := supportReplyView(locale, reply)
		if err := notifier.sender.Send(ctx, reply.CustomerID, reply.TelegramID, view); err != nil {
			notifier.logger.Warn("operator reply delivery failed", "ticket_id", reply.TicketID, "error", err)
			continue
		}
		// Delivery is marked only after Telegram accepted the message, and the
		// mark is what raises the unread counter, so a retry cannot double-count.
		if err := notifier.store.MarkOperatorReplyDelivered(ctx, reply.MessageID); err != nil {
			notifier.logger.Error("operator reply delivery bookkeeping failed", "ticket_id", reply.TicketID, "error", err)
		}
	}
}

// deliverCampaigns sends the next slice of every running campaign.
//
// Every recipient goes through the same delivery policy a lifecycle alert does:
// consent, quiet hours, the marketing frequency cap, and Telegram delivery
// health. That is the reason delivery lives here rather than in the campaign
// runner — duplicating the policy would mean two places that can disagree about
// whether a customer may be messaged.
//
// A recipient the policy refuses is recorded as suppressed with the reason,
// never silently dropped. An operator reviewing a campaign's reach needs to see
// that four hundred people were on the list and not contacted, and why.
func (notifier *Notifier) deliverCampaigns(ctx context.Context) {
	messages, err := notifier.store.PendingCampaignMessages(ctx, notificationBatch)
	if err != nil {
		notifier.logger.Error("campaign message lookup failed", "error", err)
		return
	}
	if len(messages) == 0 {
		return
	}
	candidates, err := notifier.store.notificationCandidates(ctx, notifier.settings.MarketingWindow)
	if err != nil {
		notifier.logger.Error("notification candidate lookup failed", "error", err)
		return
	}
	byCustomer := make(map[string]notificationCandidate, len(candidates))
	for _, candidate := range candidates {
		byCustomer[candidate.UserID] = candidate
	}

	for _, message := range messages {
		if ctx.Err() != nil {
			return
		}
		candidate, known := byCustomer[message.CustomerID]
		if !known {
			// The customer is no longer reachable through Telegram at all.
			notifier.resolveCampaign(ctx, message, "suppressed", "no_telegram", "")
			continue
		}
		policy := commerce.DeliveryPolicy{
			KindEnabled:           kindEnabled("marketing", candidate),
			MarketingConsent:      candidate.MarketingEnabled,
			QuietHours:            candidate.quietHours(),
			MarketingSentInWindow: candidate.MarketingSent,
			MarketingFrequencyCap: notifier.settings.MarketingFrequencyCap,
			FrequencyWindow:       notifier.settings.MarketingWindow,
			DeliveryStatus:        candidate.DeliveryStatus,
			RetryAfter:            candidate.RetryAfter.Time,
		}
		decision, err := commerce.EvaluateDelivery(notifier.clock().UTC(), message.Class, policy)
		if err != nil {
			notifier.logger.Error("campaign classification failed", "error", err)
			continue
		}
		if !decision.Allow {
			notifier.resolveCampaign(ctx, message, "suppressed", campaignSuppression(decision), "")
			continue
		}

		body := renderTemplate(message.Body, map[string]string{})
		view := View{Text: body}
		if message.Subject != "" {
			view.Text = "<b>" + html.EscapeString(message.Subject) + "</b>\n\n" + body
		}
		if sendErr := notifier.sender.Send(
			ctx, message.CustomerID, message.TelegramID, view,
		); sendErr != nil {
			notifier.resolveCampaign(ctx, message, "failed", "", "send_failed")
			continue
		}
		notifier.resolveCampaign(ctx, message, "sent", "", "")
	}
}

// resolveCampaign records one recipient's outcome, logging rather than
// retrying: the message has already been sent by the time this runs, and a
// retry would send it again.
func (notifier *Notifier) resolveCampaign(
	ctx context.Context, message PendingCampaignMessage, status, suppression, errorCode string,
) {
	if err := notifier.store.ResolveCampaignRecipient(
		ctx, message.CampaignID, message.CustomerID, status, suppression, errorCode,
	); err != nil {
		notifier.logger.Error("campaign bookkeeping failed",
			"campaignId", message.CampaignID, "error", err)
	}
}

// campaignSuppression maps a delivery decision onto the reason the campaign
// records. The reasons are kept apart because they are different decisions and
// an operator reviewing reach needs to tell them apart.
func campaignSuppression(decision commerce.DeliveryDecision) string {
	switch decision.Reason {
	case "deferred":
		// Quiet hours defer rather than refuse. A campaign does not hold a
		// message until morning — it records that this recipient was inside
		// their quiet window, and the operator can send again later.
		return "quiet_hours"
	case "frequency_cap":
		return "frequency_cap"
	case "bot_blocked", "user_deactivated":
		return "delivery_blocked"
	default:
		return "no_consent"
	}
}

// deliverDunning tells customers about failed automatic charges.
//
// It bypasses the quiet-hours and preference machinery the lifecycle alerts go
// through, and deliberately so: this is a payment notice about the customer's
// own subscription, in the same class as a receipt. A customer who is about to
// lose access because a card was declined is not served by holding the message
// until morning, and there is no opt-out for it in the preference model either.
func (notifier *Notifier) deliverDunning(ctx context.Context) {
	notices, err := notifier.store.PendingDunningNotices(ctx, notificationBatch)
	if err != nil {
		notifier.logger.Error("dunning notice lookup failed", "error", err)
		return
	}
	for _, notice := range notices {
		if ctx.Err() != nil {
			return
		}
		view := dunningAlertView(notifier.notices, localeFrom(notice.Locale), notice.Abandoned)
		if err := notifier.sender.Send(ctx, notice.CustomerID, notice.TelegramID, view); err != nil {
			// Leaving the mark unset is what retries it. A customer who has
			// blocked the bot is handled by the delivery state the sender keeps.
			notifier.logger.Warn("dunning notice delivery failed",
				"attemptId", notice.AttemptID, "error", err)
			continue
		}
		if err := notifier.store.MarkDunningNotified(ctx, notice.AttemptID); err != nil {
			notifier.logger.Error("dunning notice bookkeeping failed",
				"attemptId", notice.AttemptID, "error", err)
		}
	}
}

func (notifier *Notifier) deliverNews(ctx context.Context, candidates []notificationCandidate) {
	announcements, err := notifier.store.PendingNewsAnnouncements(ctx, 7*24*time.Hour, notificationBatch)
	if err != nil {
		notifier.logger.Error("news announcement lookup failed", "error", err)
		return
	}
	if len(announcements) == 0 {
		return
	}
	byCustomer := make(map[string]notificationCandidate, len(candidates))
	for _, candidate := range candidates {
		byCustomer[candidate.UserID] = candidate
	}
	for _, announcement := range announcements {
		candidate, found := byCustomer[announcement.CustomerID]
		if !found || ctx.Err() != nil {
			continue
		}
		locale := localeFrom(announcement.Locale)
		// News and announcements are installation-wide, so they carry no
		// subscription and stay deduplicated per customer.
		notifier.deliver(ctx, candidate, "", announcement.Category, announcement.PostID, newsAlertView(locale, announcement))
	}
}

// deliver claims, evaluates, and sends one notification. Every outcome is
// durable: sent, deferred to the end of quiet hours, suppressed with a reason,
// or failed with a classified error code.
func (notifier *Notifier) deliver(ctx context.Context, candidate notificationCandidate, subscriptionID, kind, dedupe string, view View) {
	claimed, err := notifier.store.claimNotification(ctx, candidate.UserID, subscriptionID, kind, dedupe)
	if err != nil {
		notifier.logger.Error("notification claim failed", "kind", kind, "error", err)
		return
	}
	if !claimed {
		return
	}
	policy := commerce.DeliveryPolicy{
		KindEnabled:           kindEnabled(kind, candidate),
		MarketingConsent:      candidate.MarketingEnabled,
		QuietHours:            candidate.quietHours(),
		MarketingSentInWindow: candidate.MarketingSent,
		MarketingFrequencyCap: notifier.settings.MarketingFrequencyCap,
		FrequencyWindow:       notifier.settings.MarketingWindow,
		DeliveryStatus:        candidate.DeliveryStatus,
		RetryAfter:            candidate.RetryAfter.Time,
	}
	decision, err := commerce.EvaluateDelivery(notifier.clock().UTC(), kind, policy)
	if err != nil {
		notifier.logger.Error("notification classification failed", "kind", kind, "error", err)
		return
	}
	if err := notifier.store.setNotificationClass(ctx, candidate.UserID, subscriptionID, kind, dedupe, string(decision.Class)); err != nil {
		notifier.logger.Error("notification classification update failed", "kind", kind, "error", err)
		return
	}
	if !decision.Allow {
		if err := notifier.store.parkNotification(ctx, candidate.UserID, subscriptionID, kind, dedupe, decision); err != nil {
			notifier.logger.Error("notification suppression bookkeeping failed", "kind", kind, "error", err)
		}
		return
	}
	sendErr := notifier.sender.Send(ctx, candidate.UserID, candidate.TelegramID, view)
	if err := notifier.store.finishNotification(ctx, candidate.UserID, subscriptionID, kind, dedupe, sendErr); err != nil {
		notifier.logger.Error("notification status update failed", "kind", kind, "error", err)
	}
}

// telegramRecipients resolves every customer the bot can message. The canonical
// Telegram identity is authoritative, and the v0.2 Remnawave mapping is unioned
// in so an installation that upgraded keeps delivering to customers who have not
// opened the bot since.
const telegramRecipients = `WITH recipient AS (
	SELECT i.user_id, (i.provider_subject)::bigint AS telegram_id
	FROM identities i
	WHERE i.provider = 'telegram' AND i.status = 'active'
	UNION
	SELECT r.user_id, r.telegram_id FROM remnawave_users r WHERE r.telegram_id IS NOT NULL
)`

func (store *PostgresStore) notificationCandidates(ctx context.Context, marketingWindow time.Duration) ([]notificationCandidate, error) {
	if marketingWindow <= 0 {
		marketingWindow = 7 * 24 * time.Hour
	}
	rows, err := store.pool.Query(ctx, telegramRecipients+`
		SELECT DISTINCT ON (u.id) u.id::text, recipient.telegram_id, COALESCE(r.remnawave_id, 0),
		CASE WHEN COALESCE(p.locale, 'auto') = 'auto' THEN u.locale ELSE p.locale END,
		COALESCE(p.expiry_notifications, true), COALESCE(p.traffic_notifications, true),
		COALESCE(p.renewal_notifications, true), COALESCE(p.news_notifications, true),
		COALESCE(p.marketing_enabled, false), p.quiet_hours_start, p.quiet_hours_end, u.timezone,
		COALESCE(d.status, 'active'), d.retry_after,
		(SELECT count(*)::integer FROM notification_deliveries n
			WHERE n.user_id = u.id AND n.class = 'marketing' AND n.status = 'sent'
			  AND n.sent_at > now() - $1::interval)
		FROM recipient
		JOIN users u ON u.id = recipient.user_id
		LEFT JOIN remnawave_users r ON r.user_id = u.id
		LEFT JOIN bot_preferences p ON p.user_id = u.id
		LEFT JOIN bot_delivery_state d ON d.user_id = u.id
		WHERE u.status = 'active'
		  AND COALESCE(d.status, 'active') NOT IN ('blocked', 'deactivated')
		LIMIT $2`, marketingWindow, notificationBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]notificationCandidate, 0, notificationBatch)
	for rows.Next() {
		var candidate notificationCandidate
		if err := rows.Scan(&candidate.UserID, &candidate.TelegramID, &candidate.RemnawaveID, &candidate.Locale,
			&candidate.ExpiryNotifications, &candidate.TrafficNotifications, &candidate.RenewalNotifications,
			&candidate.NewsNotifications, &candidate.MarketingEnabled, &candidate.QuietHoursStart,
			&candidate.QuietHoursEnd, &candidate.Timezone, &candidate.DeliveryStatus, &candidate.RetryAfter,
			&candidate.MarketingSent); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// claimNotification reserves one delivery. A deferred message becomes claimable
// again once its quiet window has passed, and a failed one only while it still
// has retries left.
func (store *PostgresStore) claimNotification(ctx context.Context, userID, subscriptionID, kind, dedupe string) (bool, error) {
	result, err := store.pool.Exec(ctx, `INSERT INTO notification_deliveries (user_id, subscription_id, kind, dedupe_key)
		VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, $4)
		ON CONFLICT (user_id, kind, subscription_id, dedupe_key) DO UPDATE
		SET status = 'pending', scheduled_at = now(), deferred_until = NULL
		WHERE (notification_deliveries.status = 'failed' AND notification_deliveries.failure_count < 3)
		   OR (notification_deliveries.status = 'deferred'
		       AND COALESCE(notification_deliveries.deferred_until, now()) <= now())`, userID, subscriptionID, kind, dedupe)
	return err == nil && result.RowsAffected() == 1, err
}

// notificationKey scopes an update to exactly one delivery row. The subscription
// is part of the key so two subscriptions of one customer never collide.
const notificationKey = `WHERE user_id = $1::uuid AND subscription_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid
	AND kind = $3 AND dedupe_key = $4`

func (store *PostgresStore) setNotificationClass(ctx context.Context, userID, subscriptionID, kind, dedupe, class string) error {
	_, err := store.pool.Exec(ctx, `UPDATE notification_deliveries SET class = $5 `+notificationKey, userID, subscriptionID, kind, dedupe, class)
	return err
}

// parkNotification records a policy outcome that is not a delivery: either a
// deferral until quiet hours end or a permanent suppression with its reason.
func (store *PostgresStore) parkNotification(ctx context.Context, userID, subscriptionID, kind, dedupe string, decision commerce.DeliveryDecision) error {
	if decision.DeferUntil.IsZero() {
		_, err := store.pool.Exec(ctx, `UPDATE notification_deliveries
			SET status = 'suppressed', error_code = $5 `+notificationKey, userID, subscriptionID, kind, dedupe, decision.Reason)
		return err
	}
	_, err := store.pool.Exec(ctx, `UPDATE notification_deliveries
		SET status = 'deferred', deferred_until = $5, error_code = NULL `+notificationKey, userID, subscriptionID, kind, dedupe, decision.DeferUntil)
	return err
}

func (store *PostgresStore) finishNotification(ctx context.Context, userID, subscriptionID, kind, dedupe string, sendErr error) error {
	if sendErr == nil {
		_, err := store.pool.Exec(ctx, `UPDATE notification_deliveries
			SET status = 'sent', sent_at = now(), error_code = NULL `+notificationKey, userID, subscriptionID, kind, dedupe)
		return err
	}
	code := "telegram_unavailable"
	var classified *DeliveryError
	if errors.As(sendErr, &classified) {
		code = classified.Code
	}
	_, err := store.pool.Exec(ctx, `UPDATE notification_deliveries
		SET status = 'failed', failure_count = failure_count + 1, error_code = $5 `+notificationKey, userID, subscriptionID, kind, dedupe, code)
	return err
}
