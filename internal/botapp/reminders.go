package botapp

import (
	"context"
	"html"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// reminderRoutes are the screens a reminder's button may open.
var reminderRoutes = map[string]string{
	"plans": routePlans, "connect": routeConnect, "wallet": routeWallet,
	"support": routeSupport, "home": routeHome,
}

// reminderView renders one reminder in the customer's language. The text is
// the operator's and is escaped: it is shown as written, never as markup.
func reminderView(locale Locale, reminder dbgen.Reminder) View {
	title, body := reminder.TitleEn, reminder.BodyEn
	if locale == LocaleRussian {
		title, body = reminder.TitleRu, reminder.BodyRu
	}
	rows := make([][]models.InlineKeyboardButton, 0, 2)
	if route, ok := reminderRoutes[reminder.ButtonRoute.String]; ok && route != routeHome {
		rows = append(rows, row(callbackButton(text(locale, "reminder.open"), route)))
	}
	rows = append(rows, row(callbackButton(text(locale, "action.menu"), routeHome)))
	return View{
		Text:     "<b>" + html.EscapeString(title) + "</b>\n\n" + html.EscapeString(body),
		Keyboard: keyboard(rows...),
	}
}

// reminderBatch bounds one reminder's audience per pass.
const reminderBatch = 200

// DeliverReminders sends every active reminder to the customers its segment
// holds and who are due for it. A delivery that was sent or suppressed counts
// against the reminder's send limit; one deferred by quiet hours is claimed
// again on a later pass under the same key and counts only when it settles.
func (notifier *Notifier) DeliverReminders(ctx context.Context) {
	if notifier.store == nil || notifier.store.pool == nil {
		return
	}
	queries := dbgen.New(notifier.store.pool)
	reminders, err := queries.ListActiveReminders(ctx)
	if err != nil {
		notifier.logger.Warn("reminder lookup failed", "error", err)
		return
	}
	lookup := notifier.candidateLookup()
	day := notifier.clock().UTC().Format(time.DateOnly)
	for _, reminder := range reminders {
		kind := "reminder"
		if reminder.Category == "marketing" {
			kind = "marketing"
		}
		after := pgtype.UUID{Valid: true}
		for ctx.Err() == nil && !notifier.paused() {
			audience, err := queries.ListReminderAudience(ctx, dbgen.ListReminderAudienceParams{
				ReminderID: reminder.ID, AfterUser: after, BatchSize: reminderBatch,
			})
			if err != nil {
				notifier.logger.Warn("reminder audience lookup failed", "error", err)
				break
			}
			for _, userID := range audience {
				after = userID
				candidate, found, lookupErr := lookup(ctx, uuidText(userID))
				if lookupErr != nil || !found {
					continue
				}
				dedupe := "reminder:" + uuidText(reminder.ID) + ":" + day
				notifier.deliver(ctx, candidate, "", kind, dedupe,
					reminderView(localeFrom(candidate.Locale), reminder))
				status, statusErr := queries.GetNotificationDeliveryStatus(ctx, dbgen.GetNotificationDeliveryStatusParams{
					UserID: userID, Kind: kind, DedupeKey: dedupe,
				})
				if statusErr != nil || (status != "sent" && status != "suppressed") {
					continue
				}
				if err := queries.RecordReminderSend(ctx, dbgen.RecordReminderSendParams{
					ReminderID: reminder.ID, UserID: userID,
				}); err != nil {
					notifier.logger.Warn("reminder bookkeeping failed", "error", err)
				}
			}
			if len(audience) < reminderBatch {
				break
			}
		}
	}
}
