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

// churnReasonCodes maps the short codes a churn button carries to the reasons
// analytics records. Callback data is capped at 64 bytes and a subscription
// identifier takes 36 of them, so the button carries two letters.
var churnReasonCodes = []struct{ code, reason string }{
	{"pr", "price"}, {"bl", "blocked"}, {"sp", "speed"}, {"nn", "not_needed"},
	{"sw", "switched"}, {"te", "technical"}, {"ot", "other"},
}

// churnOccasionCodes are the occasions a churn button can name.
var churnOccasionCodes = map[string]string{
	"o": analytics.OccasionAutoRenewOff,
	"l": analytics.OccasionLapsed,
}

// churnQuestionView asks why the customer is leaving, one button per reason
// and a way past the question: answering is never required.
func churnQuestionView(locale Locale, intro, occasionCode, subscriptionID string, skip models.InlineKeyboardButton) View {
	rows := make([][]models.InlineKeyboardButton, 0, len(churnReasonCodes)/2+2)
	var pair []models.InlineKeyboardButton
	for _, entry := range churnReasonCodes {
		reasonCode := entry.code
		pair = append(pair, actionButton(text(locale, "churn.reason."+entry.reason),
			churnAction(occasionCode, reasonCode, subscriptionID)))
		if len(pair) == 2 {
			rows = append(rows, row(pair...))
			pair = nil
		}
	}
	if len(pair) > 0 {
		rows = append(rows, row(pair...))
	}
	rows = append(rows, row(skip))
	body := text(locale, "churn.ask")
	if intro != "" {
		body = intro + "\n\n" + body
	}
	return View{Text: body, Keyboard: keyboard(rows...)}
}

// churnAction is the callback of one answer: the occasion's letter, the
// reason's two letters, and the subscription when there is one.
func churnAction(occasionCode, reasonCode, subscriptionID string) string {
	if subscriptionID == "" {
		return "churn:" + occasionCode + ":" + reasonCode
	}
	return "churn:" + occasionCode + ":" + reasonCode + ":" + subscriptionID
}

// askChurnReason opens the question from a message that only offered it, such
// as the notice that a subscription expired.
func (app *App) askChurnReason(session commerceContext, occasion, subscriptionID string) View {
	if app.analytics == nil || churnOccasionCodes[occasion] == "" {
		return app.errorView(session.Locale, routeHome)
	}
	return churnQuestionView(session.Locale, "", occasion, subscriptionID,
		callbackButton(text(session.Locale, "action.menu"), routeHome))
}

// recordChurnReason stores the customer's answer and thanks them. An answer
// that cannot be stored is not the customer's problem, and they are thanked
// all the same; the failure is logged.
func (app *App) recordChurnReason(ctx context.Context, session commerceContext, occasion, code, subscriptionID string) View {
	back := keyboard(row(callbackButton(text(session.Locale, "action.menu"), routeHome)))
	reason := ""
	for _, entry := range churnReasonCodes {
		if entry.code == code {
			reason = entry.reason
		}
	}
	occasionName := churnOccasionCodes[occasion]
	if app.analytics == nil || reason == "" || occasionName == "" {
		return app.errorView(session.Locale, routeHome)
	}
	err := app.analytics.RecordChurnReason(ctx, session.Customer.ID, subscriptionID, occasionName, reason, analytics.SurfaceBot)
	if err != nil && !errors.Is(err, analytics.ErrNotFound) && !errors.Is(err, analytics.ErrInvalid) {
		app.logError(ctx, "churn reason not recorded", "error", err)
	}
	return View{Text: text(session.Locale, "churn.thanks"), Keyboard: back}
}
