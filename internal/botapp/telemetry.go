package botapp

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
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
			outcome := &updateOutcome{}
			ctx = context.WithValue(ctx, updateOutcomeKey{}, outcome)
			started := time.Now()
			result := "handled"
			func() {
				// The Telegram library runs handlers without a recover, so a
				// panicking handler would take the whole bot down with it.
				defer func() {
					if recovered := recover(); recovered != nil {
						result = "panicked"
						slog.ErrorContext(ctx, "telegram handler panicked",
							"kind", kind, "update_id", updateID, "panic", platform.Redact(fmt.Sprint(recovered)), "stack", string(debug.Stack()))
					}
				}()
				next(ctx, client, update)
			}()
			if result == "handled" && outcome.failed.Load() {
				result = "failed"
			}
			if result == "handled" {
				span.SetStatus(codes.Ok, "")
			} else {
				span.SetStatus(codes.Error, result)
			}
			if metrics != nil {
				metrics.ObserveTelegramUpdate(kind, result)
				metrics.JobDuration.WithLabelValues("telegram.update").Observe(time.Since(started).Seconds())
				metrics.Jobs.WithLabelValues("telegram.update", result).Inc()
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

// updateOutcomeKey carries the current update's outcome through its context.
type updateOutcomeKey struct{}

type updateOutcome struct{ failed atomic.Bool }

// markUpdateFailed records that the update being handled ended in an error.
// Outside an update (a background send, a test) it does nothing.
func markUpdateFailed(ctx context.Context) {
	if outcome, ok := ctx.Value(updateOutcomeKey{}).(*updateOutcome); ok {
		outcome.failed.Store(true)
	}
}

// logError logs a handler failure and marks the update as failed, so the
// update counter and the span tell a failing handler apart from a handled one.
func (app *App) logError(ctx context.Context, msg string, args ...any) {
	markUpdateFailed(ctx)
	app.logger.ErrorContext(ctx, msg, args...)
}
