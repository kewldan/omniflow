package botapp

import (
	"context"
	"html"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/notice"
)

// maintenanceBroadcastWindow is how long a maintenance notice keeps going out.
// A notice about maintenance that ended a day ago is noise, so a customer the
// bot could not reach in that time is not told late.
const maintenanceBroadcastWindow = 24 * time.Hour

// pendingMaintenanceNotice is one customer still owed the latest maintenance
// notice.
type pendingMaintenanceNotice struct {
	BroadcastID      string
	Transition       string
	NoticeRU         string
	NoticeEN         string
	ExpectedReturnAt pgtype.Timestamptz
	CustomerID       string
	Locale           string
}

// pendingMaintenanceNotices finds the customers who have not yet been sent the
// most recent maintenance notice. Only the latest one counts: when maintenance
// flickers on and off, a customer not yet told it started is told it ended
// rather than both. Consent, the news preference, quiet hours, and the
// operator's per-kind switch are applied by the delivery pipeline, exactly as
// for an announcement; customers the bot cannot reach are not candidates.
func (store *PostgresStore) pendingMaintenanceNotices(ctx context.Context, limit int) ([]pendingMaintenanceNotice, error) {
	rows, err := store.pool.Query(ctx, telegramRecipients+`,
		latest AS (
			SELECT * FROM maintenance_broadcasts
			WHERE created_at > now() - $1::interval
			ORDER BY created_at DESC LIMIT 1
		)
		SELECT DISTINCT ON (u.id) b.id::text, b.transition, b.notice_ru, b.notice_en, b.expected_return_at,
			u.id::text, CASE WHEN COALESCE(p.locale, 'auto') = 'auto' THEN u.locale ELSE p.locale END
		FROM latest b
		JOIN users u ON u.status = 'active'
		JOIN recipient ON recipient.user_id = u.id
		LEFT JOIN bot_preferences p ON p.user_id = u.id
		LEFT JOIN bot_delivery_state s ON s.user_id = u.id
		WHERE COALESCE(s.status, 'active') NOT IN ('blocked', 'deactivated', 'not_started')
		  AND NOT EXISTS (
			SELECT 1 FROM notification_deliveries d
			WHERE d.user_id = u.id AND d.kind = 'maintenance' AND d.dedupe_key = b.id::text
			  AND NOT `+reclaimableDelivery+`)
		ORDER BY u.id LIMIT $2`, maintenanceBroadcastWindow, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pending := make([]pendingMaintenanceNotice, 0, limit)
	for rows.Next() {
		var item pendingMaintenanceNotice
		if err := rows.Scan(&item.BroadcastID, &item.Transition, &item.NoticeRU, &item.NoticeEN,
			&item.ExpectedReturnAt, &item.CustomerID, &item.Locale); err != nil {
			return nil, err
		}
		pending = append(pending, item)
	}
	return pending, rows.Err()
}

// deliverMaintenance sends one batch of the latest maintenance notice and
// returns how many customers the batch held. Every message goes through
// deliver, so it is claimed, rate-limited, and recorded like any other.
func (notifier *Notifier) deliverMaintenance(ctx context.Context) int {
	// A kind the operator switched off is never claimed, so the batch would
	// come back unchanged and the drain would spin on it.
	if !notifier.settings.Notifications.enabled("maintenance") {
		return 0
	}
	pending, err := notifier.store.pendingMaintenanceNotices(ctx, notificationBatch)
	if err != nil {
		notifier.logger.Error("maintenance notice lookup failed", "error", err)
		return 0
	}
	lookup := notifier.candidateLookup()
	for _, item := range pending {
		if ctx.Err() != nil || notifier.paused() {
			return len(pending)
		}
		candidate, found, lookupErr := lookup(ctx, item.CustomerID)
		if lookupErr != nil {
			notifier.logger.Error("notification candidate lookup failed", "error", lookupErr)
			return len(pending)
		}
		if !found {
			if err := notifier.store.suppressUnreachable(ctx, item.CustomerID, "",
				"maintenance", item.BroadcastID, "unreachable"); err != nil {
				notifier.logger.Error("maintenance suppression bookkeeping failed", "error", err)
			}
			continue
		}
		view := maintenanceNoticeView(notifier.notices, localeFrom(item.Locale), item)
		notifier.deliver(ctx, candidate, "", "maintenance", item.BroadcastID, view)
	}
	return len(pending)
}

// maintenanceNoticeView renders a maintenance notice in the operator's
// wording. The notice text is the operator's plain text, so it is escaped
// before it enters the HTML message.
func maintenanceNoticeView(set notices, locale Locale, item pendingMaintenanceNotice) View {
	if item.Transition != "activated" {
		return View{Text: set.text(locale, notice.CodeMaintenanceEnded, nil), Keyboard: keyboard(
			row(callbackButton(text(locale, "action.menu"), routeHome)),
		)}
	}
	body := item.NoticeEN
	if locale == LocaleRussian && item.NoticeRU != "" || body == "" {
		body = item.NoticeRU
	}
	until := text(locale, "maintenance.soon")
	if item.ExpectedReturnAt.Valid {
		until = item.ExpectedReturnAt.Time.UTC().Format("02.01.2006 15:04") + " UTC"
	}
	return View{Text: set.text(locale, notice.CodeMaintenanceStarted, map[string]string{
		"notice": html.EscapeString(body), "until": until,
	})}
}
