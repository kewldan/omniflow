package botapp

import (
	"context"
	"errors"
	"fmt"
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
	notifier.deliverSupportReplies(ctx)
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
	if err := notifier.store.PurgeExpiredAttachments(ctx); err != nil {
		notifier.logger.Warn("attachment retention cleanup failed", "error", err)
	}
}

func (notifier *Notifier) deliverLifecycle(ctx context.Context, candidate notificationCandidate) {
	locale := localeFrom(candidate.Locale)
	if candidate.RemnawaveID > 0 {
		user, lookupErr := notifier.remnawave.User(ctx, candidate.RemnawaveID)
		if lookupErr != nil {
			notifier.logger.Warn("notification user lookup failed", "error", lookupErr)
		} else {
			notifier.maybeSendExpiry(ctx, candidate, user, locale)
			notifier.maybeSendTraffic(ctx, candidate, user, locale)
		}
	}
	if notifier.commerce == nil {
		return
	}
	entitlement, err := notifier.store.Entitlement(ctx, candidate.UserID, locale, notifier.settings.Currency)
	if err != nil {
		notifier.logger.Warn("entitlement lookup failed for notifications", "error", err)
		return
	}
	if !entitlement.Found {
		return
	}
	now := notifier.clock().UTC()
	if offset, due := commerce.RenewalReminderDue(now, entitlement.EndsAt); due {
		dedupe := fmt.Sprintf("%s:%d", entitlement.ID, offset)
		notifier.deliver(ctx, candidate, "renewal", dedupe, renewalReminderView(locale, entitlement, offset))
	}
	if entitlement.GracePeriod > 0 && !now.Before(entitlement.EndsAt) && now.Before(entitlement.EndsAt.Add(entitlement.GracePeriod)) {
		notifier.deliver(ctx, candidate, "grace", entitlement.ID, gracePeriodView(locale, entitlement))
	}
	if commerce.RecoveryDue(now, entitlement.EndsAt, entitlement.GracePeriod, notifier.settings.RecoveryWindow) {
		notifier.deliver(ctx, candidate, "recovery", entitlement.ID, recoveryView(locale, entitlement))
	}
	notifier.deliverFulfillment(ctx, candidate, entitlement, locale)
}

// deliverFulfillment tells a customer when provisioning finished, or when it is
// taking long enough that they deserve an explanation. The payment is never at
// risk in either case.
func (notifier *Notifier) deliverFulfillment(ctx context.Context, candidate notificationCandidate, entitlement Entitlement, locale Locale) {
	status, err := notifier.store.LatestFulfillmentStatus(ctx, entitlement.ID)
	if err != nil {
		notifier.logger.Warn("fulfillment status lookup failed", "error", err)
		return
	}
	switch status {
	case "succeeded":
		notifier.deliver(ctx, candidate, "fulfillment", entitlement.ID+":succeeded", fulfillmentAlertView(locale, true))
	case "failed":
		notifier.deliver(ctx, candidate, "fulfillment", entitlement.ID+":failed", fulfillmentAlertView(locale, false))
	}
}

func (notifier *Notifier) maybeSendExpiry(ctx context.Context, candidate notificationCandidate, user remnawave.User, locale Locale) {
	days := int(math.Ceil(time.Until(user.ExpireAt).Hours() / 24))
	if days < 0 {
		days = 0
	}
	if days != 7 && days != 3 && days != 1 && days != 0 {
		return
	}
	dedupe := fmt.Sprintf("%s:%d", user.ExpireAt.UTC().Format(time.DateOnly), days)
	notifier.deliver(ctx, candidate, "expiry", dedupe, expiryAlertView(locale, days))
}

func (notifier *Notifier) maybeSendTraffic(ctx context.Context, candidate notificationCandidate, user remnawave.User, locale Locale) {
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
	notifier.deliver(ctx, candidate, "traffic", dedupe, trafficAlertView(locale, percent))
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
		notifier.deliver(ctx, candidate, announcement.Category, announcement.PostID, newsAlertView(locale, announcement))
	}
}

// deliver claims, evaluates, and sends one notification. Every outcome is
// durable: sent, deferred to the end of quiet hours, suppressed with a reason,
// or failed with a classified error code.
func (notifier *Notifier) deliver(ctx context.Context, candidate notificationCandidate, kind, dedupe string, view View) {
	claimed, err := notifier.store.claimNotification(ctx, candidate.UserID, kind, dedupe)
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
	if err := notifier.store.setNotificationClass(ctx, candidate.UserID, kind, dedupe, string(decision.Class)); err != nil {
		notifier.logger.Error("notification classification update failed", "kind", kind, "error", err)
		return
	}
	if !decision.Allow {
		if err := notifier.store.parkNotification(ctx, candidate.UserID, kind, dedupe, decision); err != nil {
			notifier.logger.Error("notification suppression bookkeeping failed", "kind", kind, "error", err)
		}
		return
	}
	sendErr := notifier.sender.Send(ctx, candidate.UserID, candidate.TelegramID, view)
	if err := notifier.store.finishNotification(ctx, candidate.UserID, kind, dedupe, sendErr); err != nil {
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
func (store *PostgresStore) claimNotification(ctx context.Context, userID, kind, dedupe string) (bool, error) {
	result, err := store.pool.Exec(ctx, `INSERT INTO notification_deliveries (user_id, kind, dedupe_key)
		VALUES ($1::uuid, $2, $3) ON CONFLICT (user_id, kind, dedupe_key) DO UPDATE
		SET status = 'pending', scheduled_at = now(), deferred_until = NULL
		WHERE (notification_deliveries.status = 'failed' AND notification_deliveries.failure_count < 3)
		   OR (notification_deliveries.status = 'deferred'
		       AND COALESCE(notification_deliveries.deferred_until, now()) <= now())`, userID, kind, dedupe)
	return err == nil && result.RowsAffected() == 1, err
}

func (store *PostgresStore) setNotificationClass(ctx context.Context, userID, kind, dedupe, class string) error {
	_, err := store.pool.Exec(ctx, `UPDATE notification_deliveries SET class = $4
		WHERE user_id = $1::uuid AND kind = $2 AND dedupe_key = $3`, userID, kind, dedupe, class)
	return err
}

// parkNotification records a policy outcome that is not a delivery: either a
// deferral until quiet hours end or a permanent suppression with its reason.
func (store *PostgresStore) parkNotification(ctx context.Context, userID, kind, dedupe string, decision commerce.DeliveryDecision) error {
	if decision.DeferUntil.IsZero() {
		_, err := store.pool.Exec(ctx, `UPDATE notification_deliveries
			SET status = 'suppressed', error_code = $4
			WHERE user_id = $1::uuid AND kind = $2 AND dedupe_key = $3`, userID, kind, dedupe, decision.Reason)
		return err
	}
	_, err := store.pool.Exec(ctx, `UPDATE notification_deliveries
		SET status = 'deferred', deferred_until = $4, error_code = NULL
		WHERE user_id = $1::uuid AND kind = $2 AND dedupe_key = $3`, userID, kind, dedupe, decision.DeferUntil)
	return err
}

func (store *PostgresStore) finishNotification(ctx context.Context, userID, kind, dedupe string, sendErr error) error {
	if sendErr == nil {
		_, err := store.pool.Exec(ctx, `UPDATE notification_deliveries SET status = 'sent', sent_at = now(), error_code = NULL
			WHERE user_id = $1::uuid AND kind = $2 AND dedupe_key = $3`, userID, kind, dedupe)
		return err
	}
	code := "telegram_unavailable"
	var classified *DeliveryError
	if errors.As(sendErr, &classified) {
		code = classified.Code
	}
	_, err := store.pool.Exec(ctx, `UPDATE notification_deliveries
		SET status = 'failed', failure_count = failure_count + 1, error_code = $4
		WHERE user_id = $1::uuid AND kind = $2 AND dedupe_key = $3`, userID, kind, dedupe, code)
	return err
}
