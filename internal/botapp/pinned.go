package botapp

import (
	"context"
	"errors"
	"strings"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
)

// pinnedMessage is the operator's message for everybody.
type pinnedMessage struct {
	ID          string
	TextRU      string
	TextEN      string
	Photo       string
	Pin         bool
	ShowOnStart bool
}

func (message pinnedMessage) view(locale Locale) View {
	body := message.TextEN
	if locale == LocaleRussian {
		body = message.TextRU
	}
	return View{Text: body, Photo: message.Photo}
}

// activePinned reads the message in force, if any.
func (store *PostgresStore) activePinned(ctx context.Context) (pinnedMessage, bool, error) {
	var message pinnedMessage
	err := store.pool.QueryRow(ctx, `SELECT id::text, text_ru, text_en, COALESCE(photo_url, ''), pin, show_on_start
		FROM pinned_messages WHERE status = 'active'`).Scan(&message.ID, &message.TextRU, &message.TextEN,
		&message.Photo, &message.Pin, &message.ShowOnStart)
	if errors.Is(err, pgx.ErrNoRows) {
		return pinnedMessage{}, false, nil
	}
	return message, err == nil, err
}

// sendPinned sends the message, pins it when asked, and returns its id.
func (sender *Sender) sendPinned(ctx context.Context, chatID int64, view View, pin bool) (int, error) {
	wait, pausedUntil := sender.pacer.reserve()
	if !pausedUntil.IsZero() {
		return 0, &DeliveryError{Code: "flood_wait", RetryAfter: pausedUntil, Err: errFloodPaused}
	}
	if wait > 0 {
		sender.sleep(ctx, wait)
	}
	var sent *models.Message
	var err error
	if view.Photo != "" && len([]rune(view.Text)) <= captionLimit {
		sent, err = sender.client.SendPhoto(ctx, &telegram.SendPhotoParams{
			ChatID: chatID, Photo: &models.InputFileString{Data: view.Photo},
			Caption: view.Text, ParseMode: models.ParseModeHTML,
		})
	} else {
		sent, err = sender.client.SendMessage(ctx, sendParams(chatID, View{Text: view.Text}))
	}
	if err != nil {
		return 0, ClassifyDelivery(sender.clock(), err)
	}
	if pin {
		if _, pinErr := sender.client.PinChatMessage(ctx, &telegram.PinChatMessageParams{
			ChatID: chatID, MessageID: sent.ID, DisableNotification: true,
		}); pinErr != nil {
			sender.logger.Debug("pinning failed", "error", pinErr)
			return sent.ID, errPinFailed
		}
	}
	return sent.ID, nil
}

var errPinFailed = errors.New("message was sent but not pinned")

// deliverPinnedTo sends the message in force to one customer once and records
// the outcome; a second call for the same customer does nothing.
func (notifier *Notifier) deliverPinnedTo(ctx context.Context, message pinnedMessage, customerID string, chatID int64, locale Locale) {
	var claimed bool
	if err := notifier.store.pool.QueryRow(ctx, `INSERT INTO pinned_message_deliveries (pinned_id, user_id, chat_id, status)
		VALUES ($1::uuid, $2::uuid, $3, 'failed') ON CONFLICT DO NOTHING RETURNING true`,
		message.ID, customerID, chatID).Scan(&claimed); err != nil {
		return
	}
	messageID, err := notifier.sender.sendPinned(ctx, chatID, message.view(locale), message.Pin)
	status, pinned, code := "sent", message.Pin, ""
	switch {
	case errors.Is(err, errPinFailed):
		pinned = false
	case err != nil:
		status, pinned = "failed", false
		var classified *DeliveryError
		if errors.As(err, &classified) {
			code = classified.Code
		}
	}
	if _, err := notifier.store.pool.Exec(ctx, `UPDATE pinned_message_deliveries
		SET status = $3, pinned = $4, telegram_message_id = NULLIF($5, 0), error_code = NULLIF($6, '')
		WHERE pinned_id = $1::uuid AND user_id = $2::uuid`, message.ID, customerID, status, pinned, messageID, code); err != nil {
		notifier.logger.Warn("pinned delivery bookkeeping failed", "error", err)
	}
}

// deliverPinned sends the message in force to one batch of reachable
// customers who have not had it, and unpins retired messages.
func (notifier *Notifier) deliverPinned(ctx context.Context) {
	notifier.unpinRetired(ctx)
	message, active, err := notifier.store.activePinned(ctx)
	if err != nil || !active {
		return
	}
	rows, err := notifier.store.pool.Query(ctx, telegramRecipients+`
		SELECT DISTINCT ON (u.id) u.id::text, recipient.telegram_id,
			CASE WHEN COALESCE(p.locale, 'auto') = 'auto' THEN u.locale ELSE p.locale END
		FROM recipient
		JOIN users u ON u.id = recipient.user_id
		LEFT JOIN bot_preferences p ON p.user_id = u.id
		LEFT JOIN bot_delivery_state d ON d.user_id = u.id
		WHERE u.status = 'active' AND COALESCE(d.status, 'active') NOT IN ('blocked', 'deactivated', 'not_started')
		  AND NOT EXISTS (SELECT 1 FROM pinned_message_deliveries m WHERE m.pinned_id = $1::uuid AND m.user_id = u.id)
		ORDER BY u.id LIMIT $2`, message.ID, notificationBatch)
	if err != nil {
		notifier.logger.Error("pinned message recipients lookup failed", "error", err)
		return
	}
	type recipient struct {
		customer string
		chat     int64
		locale   string
	}
	recipients := []recipient{}
	for rows.Next() {
		var item recipient
		if err := rows.Scan(&item.customer, &item.chat, &item.locale); err != nil {
			rows.Close()
			return
		}
		recipients = append(recipients, item)
	}
	rows.Close()
	for _, item := range recipients {
		if ctx.Err() != nil || notifier.paused() {
			return
		}
		notifier.deliverPinnedTo(ctx, message, item.customer, item.chat, localeFrom(item.locale))
	}
}

// unpinRetired unpins one batch of a retired message's pins.
func (notifier *Notifier) unpinRetired(ctx context.Context) {
	rows, err := notifier.store.pool.Query(ctx, `SELECT m.pinned_id::text, m.user_id::text, m.chat_id, m.telegram_message_id
		FROM pinned_message_deliveries m JOIN pinned_messages p ON p.id = m.pinned_id
		WHERE p.status = 'retired' AND m.pinned AND m.unpinned_at IS NULL AND m.telegram_message_id IS NOT NULL
		LIMIT $1`, notificationBatch)
	if err != nil {
		return
	}
	type pin struct {
		pinned, customer string
		chat, message    int64
	}
	pins := []pin{}
	for rows.Next() {
		var item pin
		if err := rows.Scan(&item.pinned, &item.customer, &item.chat, &item.message); err != nil {
			rows.Close()
			return
		}
		pins = append(pins, item)
	}
	rows.Close()
	for _, item := range pins {
		_, err := notifier.sender.client.UnpinChatMessage(ctx, &telegram.UnpinChatMessageParams{ChatID: item.chat, MessageID: int(item.message)})
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "not found") {
			notifier.logger.Debug("unpinning failed", "error", err)
		}
		_, _ = notifier.store.pool.Exec(ctx, `UPDATE pinned_message_deliveries SET unpinned_at = now()
			WHERE pinned_id = $1::uuid AND user_id = $2::uuid`, item.pinned, item.customer)
	}
}

// showPinnedOnStart sends the message in force to a customer opening the bot
// who has not had it yet.
func (app *App) showPinnedOnStart(ctx context.Context, chatID int64, from *models.User, locale Locale) {
	if !app.commerceEnabled() || app.sender == nil || from == nil {
		return
	}
	message, active, err := app.customers.activePinned(ctx)
	if err != nil || !active || !message.ShowOnStart {
		return
	}
	session, err := app.commerceContext(ctx, from.ID, from.LanguageCode)
	if err != nil {
		return
	}
	notifier := &Notifier{logger: app.logger, sender: app.sender, store: app.customers}
	notifier.deliverPinnedTo(ctx, message, session.Customer.ID, chatID, locale)
}
