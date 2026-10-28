package botapp

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/commerce"
)

// maxFloodWait bounds how long a delivery attempt will honour a Telegram flood
// wait inline. A longer wait is deferred to the next pass instead of blocking.
const maxFloodWait = 30 * time.Second

const deliveryAttempts = 3

// DeliveryError is a classified Telegram failure.
type DeliveryError struct {
	Code       string
	RetryAfter time.Time
	Err        error
}

func (err *DeliveryError) Error() string { return err.Code + ": " + err.Err.Error() }
func (err *DeliveryError) Unwrap() error { return err.Err }

// Sender delivers bot messages with retry, flood-wait handling, and durable
// classification of failures. A customer who blocked the bot or deleted their
// account stops consuming retries entirely.
type Sender struct {
	client *telegram.Bot
	store  *PostgresStore
	logger *slog.Logger
	clock  func() time.Time
	sleep  func(context.Context, time.Duration)
	pacer  *deliveryPacer
}

// NewSender builds the delivery wrapper used by every outbound bot message. It
// paces sends at DefaultDeliveryRate until SetRate says otherwise.
func NewSender(client *telegram.Bot, store *PostgresStore, logger *slog.Logger) *Sender {
	return &Sender{
		client: client, store: store, logger: logger, clock: time.Now, sleep: sleepContext,
		pacer: newDeliveryPacer(DefaultDeliveryRate, time.Now),
	}
}

// SetRate changes how many messages a second the sender may send. Zero or less
// keeps the default.
func (sender *Sender) SetRate(perSecond int) {
	sender.pacer.setRate(perSecond)
}

// Paused reports whether Telegram has paused the bot for longer than a send
// may wait, and until when. A delivery pass checks it to stop early instead of
// walking its whole batch into the same refusal.
func (sender *Sender) Paused() (time.Time, bool) {
	return sender.pacer.paused()
}

// Send delivers one view to a chat and records the delivery outcome against the
// customer when one is known.
//
// Every attempt draws from the bot-wide pacer first. A flood wait is the bot's,
// not the customer's: it pauses every send until Telegram's retry_after and is
// never recorded against the customer's delivery state. A pause longer than a
// send may wait returns a flood error carrying the instant it ends, which the
// callers record as a deferral rather than a failure.
func (sender *Sender) Send(ctx context.Context, customerID string, chatID int64, view View) error {
	var lastErr error
	for attempt := range deliveryAttempts {
		wait, pausedUntil := sender.pacer.reserve()
		if !pausedUntil.IsZero() {
			return &DeliveryError{Code: "flood_wait", RetryAfter: pausedUntil, Err: errFloodPaused}
		}
		if wait > 0 {
			sender.sleep(ctx, wait)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		_, err := sender.client.SendMessage(ctx, sendParams(chatID, view))
		if err == nil {
			if customerID != "" {
				if recordErr := sender.store.RecordDeliverySuccess(ctx, customerID); recordErr != nil {
					sender.logger.Warn("delivery state update failed", "error", recordErr)
				}
			}
			return nil
		}
		lastErr = err
		classified := ClassifyDelivery(sender.clock(), err)
		if classified.Code == "flood_wait" {
			// The whole bot waits; the next attempt's reservation does the
			// waiting, or returns at once when the pause is too long to sit out.
			sender.pacer.pause(classified.RetryAfter)
			if attempt == deliveryAttempts-1 {
				return classified
			}
			continue
		}
		if customerID != "" {
			if recordErr := sender.store.RecordDeliveryFailure(ctx, customerID, classified.Code, classified.RetryAfter); recordErr != nil {
				sender.logger.Warn("delivery state update failed", "error", recordErr)
			}
		}
		if !retryableDelivery(classified.Code) || attempt == deliveryAttempts-1 {
			return classified
		}
		sender.sleep(ctx, time.Duration(attempt+1)*time.Second)
		if ctx.Err() != nil {
			return classified
		}
	}
	return lastErr
}

// errFloodPaused is the cause of a send refused because Telegram paused the
// bot for longer than a send may wait.
var errFloodPaused = errors.New("telegram paused the bot")

// floodDeferral reports whether a send failed because the bot is paused, and
// until when the message should wait.
func floodDeferral(err error) (time.Time, bool) {
	var classified *DeliveryError
	if errors.As(err, &classified) && classified.Code == "flood_wait" {
		retryAfter := classified.RetryAfter
		if retryAfter.IsZero() {
			retryAfter = time.Now().Add(time.Minute)
		}
		return retryAfter, true
	}
	return time.Time{}, false
}

// ClassifyDelivery maps a Telegram client error onto a stable failure code and,
// for a flood wait, the instant at which delivery may be retried.
func ClassifyDelivery(now time.Time, err error) *DeliveryError {
	var flood *telegram.TooManyRequestsError
	if errors.As(err, &flood) {
		return &DeliveryError{Code: "flood_wait", RetryAfter: now.Add(time.Duration(flood.RetryAfter) * time.Second), Err: err}
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "bot was blocked by the user"), strings.Contains(message, "user is blocked"):
		return &DeliveryError{Code: "bot_blocked", Err: err}
	case strings.Contains(message, "can't initiate conversation"), strings.Contains(message, "cannot initiate conversation"):
		// A 403 like a block, and recorded apart from one: this customer never
		// pressed Start, which is not the same as having turned the bot away.
		return &DeliveryError{Code: "not_started", Err: err}
	case strings.Contains(message, "user is deactivated"):
		return &DeliveryError{Code: "user_deactivated", Err: err}
	case strings.Contains(message, "chat not found"), errors.Is(err, telegram.ErrorNotFound):
		return &DeliveryError{Code: "chat_not_found", Err: err}
	case errors.Is(err, telegram.ErrorForbidden):
		return &DeliveryError{Code: "bot_blocked", Err: err}
	case errors.Is(err, telegram.ErrorTooManyRequests):
		return &DeliveryError{Code: "flood_wait", RetryAfter: now.Add(time.Second), Err: err}
	case errors.Is(err, telegram.ErrorBadRequest):
		return &DeliveryError{Code: "rejected", Err: err}
	case errors.Is(err, context.DeadlineExceeded):
		return &DeliveryError{Code: "timeout", Err: err}
	default:
		return &DeliveryError{Code: "telegram_unavailable", Err: err}
	}
}

func retryableDelivery(code string) bool {
	_, retryable := commerce.ClassifyTelegramFailure(code)
	return retryable
}

// Edit replaces the content of an existing message, tolerating Telegram's
// "message is not modified" response, which is not an error for the customer.
func (sender *Sender) Edit(ctx context.Context, chatID int64, messageID int, view View) error {
	_, err := sender.client.EditMessageText(ctx, editParams(chatID, messageID, view))
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
		return nil
	}
	return err
}

// Answer acknowledges a callback query with an optional short toast.
func (sender *Sender) Answer(ctx context.Context, queryID, text string, alert bool) {
	if _, err := sender.client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{CallbackQueryID: queryID, Text: text, ShowAlert: alert}); err != nil {
		sender.logger.Debug("callback acknowledgement failed", "error", err)
	}
}

// Typing shows the Telegram typing indicator while a screen loads.
func (sender *Sender) Typing(ctx context.Context, chatID int64) {
	_, _ = sender.client.SendChatAction(ctx, &telegram.SendChatActionParams{ChatID: chatID, Action: models.ChatActionTyping})
}

func sleepContext(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
