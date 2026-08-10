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
}

// NewSender builds the delivery wrapper used by every outbound bot message.
func NewSender(client *telegram.Bot, store *PostgresStore, logger *slog.Logger) *Sender {
	return &Sender{client: client, store: store, logger: logger, clock: time.Now, sleep: sleepContext}
}

// Send delivers one view to a chat and records the delivery outcome against the
// customer when one is known.
func (sender *Sender) Send(ctx context.Context, customerID string, chatID int64, view View) error {
	var lastErr error
	for attempt := range deliveryAttempts {
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
		if customerID != "" {
			if recordErr := sender.store.RecordDeliveryFailure(ctx, customerID, classified.Code, classified.RetryAfter); recordErr != nil {
				sender.logger.Warn("delivery state update failed", "error", recordErr)
			}
		}
		if !retryableDelivery(classified.Code) {
			return classified
		}
		wait := time.Duration(attempt+1) * time.Second
		if remaining := classified.RetryAfter.Sub(sender.clock()); remaining > 0 {
			if remaining > maxFloodWait {
				return classified
			}
			wait = remaining
		}
		if attempt == deliveryAttempts-1 {
			return classified
		}
		sender.sleep(ctx, wait)
		if ctx.Err() != nil {
			return classified
		}
	}
	return lastErr
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
