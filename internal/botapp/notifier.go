package botapp

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/omniflow/omniflow/internal/remnawave"
)

const notificationInterval = 6 * time.Hour

type notificationCandidate struct {
	UserID               string
	TelegramID           int64
	RemnawaveID          int64
	Locale               string
	ExpiryNotifications  bool
	TrafficNotifications bool
}

// RunNotifications sends idempotent service alerts until the context is cancelled.
func RunNotifications(ctx context.Context, logger *slog.Logger, client *telegram.Bot, store *PostgresStore, service remnawave.Service) {
	runNotifications(ctx, logger, client, store, service)
	ticker := time.NewTicker(notificationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runNotifications(ctx, logger, client, store, service)
		}
	}
}

func runNotifications(ctx context.Context, logger *slog.Logger, client *telegram.Bot, store *PostgresStore, service remnawave.Service) {
	candidates, err := store.notificationCandidates(ctx)
	if err != nil {
		logger.Error("notification candidate lookup failed", "error", err)
		return
	}
	for _, candidate := range candidates {
		user, lookupErr := service.User(ctx, candidate.RemnawaveID)
		if lookupErr != nil {
			logger.Warn("notification user lookup failed", "remnawave_user_id", candidate.RemnawaveID, "error", lookupErr)
			continue
		}
		locale := localeFrom(candidate.Locale)
		if candidate.ExpiryNotifications {
			maybeSendExpiry(ctx, logger, client, store, candidate, user, locale)
		}
		if candidate.TrafficNotifications {
			maybeSendTraffic(ctx, logger, client, store, candidate, user, locale)
		}
	}
}

func maybeSendExpiry(ctx context.Context, logger *slog.Logger, client *telegram.Bot, store *PostgresStore, candidate notificationCandidate, user remnawave.User, locale Locale) {
	days := int(math.Ceil(time.Until(user.ExpireAt).Hours() / 24))
	if days < 0 {
		days = 0
	}
	if days != 7 && days != 3 && days != 1 && days != 0 {
		return
	}
	dedupe := fmt.Sprintf("%s:%d", user.ExpireAt.UTC().Format(time.DateOnly), days)
	text := fmt.Sprintf("⏳ <b>Your subscription expires in %d day(s)</b>\n\nOpen Omniflow to review your status.", days)
	if locale == LocaleRussian {
		text = fmt.Sprintf("⏳ <b>Подписка закончится через %d дн.</b>\n\nОткройте Omniflow, чтобы проверить статус.", days)
	}
	sendNotification(ctx, logger, client, store, candidate, "expiry", dedupe, text)
}

func maybeSendTraffic(ctx context.Context, logger *slog.Logger, client *telegram.Bot, store *PostgresStore, candidate notificationCandidate, user remnawave.User, locale Locale) {
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
	text := fmt.Sprintf("📡 <b>You have used %d%% of your traffic allowance</b>\n\nOpen Omniflow for details.", percent)
	if locale == LocaleRussian {
		text = fmt.Sprintf("📡 <b>Использовано %d%% доступного трафика</b>\n\nОткройте Omniflow для подробностей.", percent)
	}
	sendNotification(ctx, logger, client, store, candidate, "traffic", dedupe, text)
}

func sendNotification(ctx context.Context, logger *slog.Logger, client *telegram.Bot, store *PostgresStore, candidate notificationCandidate, kind, dedupe, message string) {
	claimed, err := store.claimNotification(ctx, candidate.UserID, kind, dedupe)
	if err != nil || !claimed {
		if err != nil {
			logger.Error("notification claim failed", "kind", kind, "error", err)
		}
		return
	}
	open := "Open"
	if candidate.Locale == "ru" {
		open = "Открыть"
	}
	_, sendErr := client.SendMessage(ctx, sendParams(candidate.TelegramID, View{Text: message, Keyboard: keyboard(row(callbackButton(open, routeHome)))}))
	if err := store.finishNotification(ctx, candidate.UserID, kind, dedupe, sendErr); err != nil {
		logger.Error("notification status update failed", "kind", kind, "error", err)
	}
}

func (store *PostgresStore) notificationCandidates(ctx context.Context) ([]notificationCandidate, error) {
	rows, err := store.pool.Query(ctx, `SELECT r.user_id::text, r.telegram_id, r.remnawave_id,
		CASE WHEN COALESCE(p.locale, 'auto') = 'auto' THEN u.locale ELSE p.locale END,
		COALESCE(p.expiry_notifications, true), COALESCE(p.traffic_notifications, true)
		FROM remnawave_users r JOIN users u ON u.id = r.user_id
		LEFT JOIN bot_preferences p ON p.user_id = r.user_id
		WHERE r.telegram_id IS NOT NULL AND u.status = 'active'
		  AND (COALESCE(p.expiry_notifications, true) OR COALESCE(p.traffic_notifications, true))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]notificationCandidate, 0)
	for rows.Next() {
		var candidate notificationCandidate
		if err := rows.Scan(&candidate.UserID, &candidate.TelegramID, &candidate.RemnawaveID, &candidate.Locale, &candidate.ExpiryNotifications, &candidate.TrafficNotifications); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func (store *PostgresStore) claimNotification(ctx context.Context, userID, kind, dedupe string) (bool, error) {
	result, err := store.pool.Exec(ctx, `INSERT INTO notification_deliveries (user_id, kind, dedupe_key)
		VALUES ($1::uuid, $2, $3) ON CONFLICT (user_id, kind, dedupe_key) DO UPDATE
		SET status = 'pending', scheduled_at = now()
		WHERE notification_deliveries.status = 'failed' AND notification_deliveries.failure_count < 3`, userID, kind, dedupe)
	return err == nil && result.RowsAffected() == 1, err
}

func (store *PostgresStore) finishNotification(ctx context.Context, userID, kind, dedupe string, sendErr error) error {
	if sendErr == nil {
		_, err := store.pool.Exec(ctx, `UPDATE notification_deliveries SET status = 'sent', sent_at = now()
			WHERE user_id = $1::uuid AND kind = $2 AND dedupe_key = $3`, userID, kind, dedupe)
		return err
	}
	_, err := store.pool.Exec(ctx, `UPDATE notification_deliveries SET status = 'failed', failure_count = failure_count + 1
		WHERE user_id = $1::uuid AND kind = $2 AND dedupe_key = $3`, userID, kind, dedupe)
	return err
}
