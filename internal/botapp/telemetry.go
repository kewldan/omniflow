package botapp

import (
	"context"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/platform"
	"go.opentelemetry.io/otel/codes"
)

// ObservabilityMiddleware wraps every Telegram update in one span and one
// metric sample, whichever handler ends up serving it.
//
// The span carries the update kind and the update ID only. A Telegram account,
// a message body, and callback data never become span attributes or metric
// labels: both are retained by whatever collects them, and neither is a safe
// place for customer content.
func ObservabilityMiddleware(metrics *platform.Metrics) telegram.Middleware {
	return func(next telegram.HandlerFunc) telegram.HandlerFunc {
		return func(ctx context.Context, client *telegram.Bot, update *models.Update) {
			kind := UpdateKind(update)
			updateID := int64(0)
			if update != nil {
				updateID = int64(update.ID)
			}
			ctx, span := platform.StartSpan(ctx, "telegram.update", platform.TelegramUpdateAttributes(updateID, kind)...)
			defer span.End()
			started := time.Now()
			next(ctx, client, update)
			span.SetStatus(codes.Ok, "")
			if metrics != nil {
				metrics.TelegramUpdates.WithLabelValues(kind, "handled").Inc()
				metrics.JobDuration.WithLabelValues("telegram.update").Observe(time.Since(started).Seconds())
			}
		}
	}
}

// UpdateKind classifies an update into a small, bounded set. Keeping the set
// closed is what stops the metric label from growing without limit.
func UpdateKind(update *models.Update) string {
	switch {
	case update == nil:
		return "unknown"
	case update.CallbackQuery != nil:
		return "callback_query"
	case update.PreCheckoutQuery != nil:
		return "pre_checkout_query"
	case update.Message != nil && update.Message.SuccessfulPayment != nil:
		return "successful_payment"
	case update.Message != nil:
		return "message"
	case update.EditedMessage != nil:
		return "edited_message"
	case update.MyChatMember != nil:
		return "my_chat_member"
	default:
		return "other"
	}
}
