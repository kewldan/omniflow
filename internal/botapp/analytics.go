package botapp

import (
	"context"
	"errors"
	"html"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"

	"github.com/omniflow/omniflow/internal/analytics"
)

// UseAnalytics attaches business analytics: the funnel steps a customer
// reaches in the bot and their mark for a resolved support ticket. Without it
// neither is recorded.
func (app *App) UseAnalytics(service *analytics.Service) {
	app.analytics = service
}

// WithAnalytics lets the notifier ask customers to rate resolved tickets.
func (notifier *Notifier) WithAnalytics(service *analytics.Service) *Notifier {
	notifier.analytics = service
	return notifier
}

// recordFunnel notes a funnel step. It never fails the screen it rides on.
func (app *App) recordFunnel(ctx context.Context, customerID, step string) {
	if app.analytics == nil || customerID == "" {
		return
	}
	if err := app.analytics.RecordFunnelStep(ctx, customerID, step, analytics.ChannelBot); err != nil {
		app.logger.Warn("funnel step not recorded", "step", step, "error", err)
	}
}

// rateTicket stores the customer's mark for one of their tickets. A second
// tap, or a tap on a ticket that is not theirs, changes nothing and says so.
func (app *App) rateTicket(ctx context.Context, session commerceContext, ticketID, mark string) View {
	back := keyboard(row(callbackButton(text(session.Locale, "action.menu"), routeHome)))
	rating, err := strconv.Atoi(mark)
	if app.analytics == nil || err != nil || rating < 1 || rating > 5 {
		return View{Text: text(session.Locale, "support.rate.already"), Keyboard: back}
	}
	taken, err := app.analytics.RecordRating(ctx, session.Customer.ID, ticketID, rating)
	if err != nil && !errors.Is(err, analytics.ErrInvalid) {
		app.logError(ctx, "support rating not recorded", "error", err)
		return app.errorView(session.Locale, routeHome)
	}
	if !taken {
		return View{Text: text(session.Locale, "support.rate.already"), Keyboard: back}
	}
	return View{Text: text(session.Locale, "support.rate.thanks"), Keyboard: back}
}

// ratingView asks for a mark from one to five, one button per mark.
func ratingView(locale Locale, ticketID, subject string) View {
	if strings.TrimSpace(subject) == "" {
		subject = shortID(ticketID)
	}
	buttons := make([]models.InlineKeyboardButton, 0, 5)
	for mark := 1; mark <= 5; mark++ {
		label := strconv.Itoa(mark) + " ⭐"
		buttons = append(buttons, actionButton(label, "csat:"+ticketID+":"+strconv.Itoa(mark)))
	}
	return View{
		Text:     text(locale, "support.rate.ask", html.EscapeString(truncateRunes(subject, 60))),
		Keyboard: keyboard(row(buttons...)),
	}
}

// deliverRatingRequests asks the customers of recently resolved tickets for
// a mark. Each ticket is asked about once: the request is recorded after the
// attempt whatever its outcome, except a flood wait, which is the bot's pause
// rather than the customer's and is tried again on the next pass.
func (notifier *Notifier) deliverRatingRequests(ctx context.Context) {
	if notifier.analytics == nil || notifier.sender == nil {
		return
	}
	requests, err := notifier.analytics.PendingRatingRequests(ctx, notificationBatch)
	if err != nil {
		notifier.logger.Error("rating request lookup failed", "error", err)
		return
	}
	for _, request := range requests {
		if ctx.Err() != nil {
			return
		}
		telegramID, locale, err := notifier.store.ratingRecipient(ctx, request.CustomerID)
		if err != nil {
			notifier.logger.Error("rating recipient lookup failed", "ticket_id", request.TicketID, "error", err)
			continue
		}
		if telegramID != 0 {
			sendErr := notifier.sender.Send(ctx, request.CustomerID, telegramID,
				ratingView(localeFrom(locale), request.TicketID, request.Subject))
			if _, deferred := floodDeferral(sendErr); deferred {
				return
			}
			if sendErr != nil {
				notifier.logger.Warn("rating request not delivered", "ticket_id", request.TicketID, "error", sendErr)
			}
		}
		if err := notifier.analytics.MarkRatingRequested(ctx, request.TicketID); err != nil {
			notifier.logger.Error("rating request bookkeeping failed", "ticket_id", request.TicketID, "error", err)
		}
	}
}

// ratingRecipient resolves the Telegram chat and interface language of an
// active customer, or a zero chat for one the bot cannot reach.
func (store *PostgresStore) ratingRecipient(ctx context.Context, customerID string) (int64, string, error) {
	var telegramID int64
	var locale string
	err := store.pool.QueryRow(ctx, telegramRecipients+`
		SELECT COALESCE(recipient.telegram_id, 0),
		  CASE WHEN COALESCE(p.locale, 'auto') = 'auto' THEN u.locale ELSE p.locale END
		FROM users u
		LEFT JOIN recipient ON recipient.user_id = u.id
		LEFT JOIN bot_preferences p ON p.user_id = u.id
		WHERE u.id = $1::uuid AND u.status = 'active'
		ORDER BY recipient.telegram_id LIMIT 1`, customerID).Scan(&telegramID, &locale)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", nil
	}
	return telegramID, locale, err
}
