package botapp

import (
	"context"
	"strconv"
	"strings"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// activityAction reduces an update to the route it asked for: a command
// ("cmd:start") or the route part of a callback ("plans", "pay"). Callback
// arguments — identifiers, amounts — are dropped, so the log holds which
// screen or button was used and nothing a customer typed.
func activityAction(update *models.Update) string {
	var raw string
	switch {
	case update == nil:
		return ""
	case update.CallbackQuery != nil:
		raw = update.CallbackQuery.Data
		if route, _, found := strings.Cut(raw, ":"); found {
			raw = route
		}
	case update.Message != nil && strings.HasPrefix(update.Message.Text, "/"):
		command, _, _ := strings.Cut(strings.TrimPrefix(update.Message.Text, "/"), " ")
		command, _, _ = strings.Cut(command, "@")
		raw = "cmd:" + command
	default:
		return ""
	}
	raw = strings.ToLower(raw)
	cleaned := strings.Builder{}
	for _, character := range raw {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || strings.ContainsRune("_:./-", character) {
			cleaned.WriteRune(character)
		}
		if cleaned.Len() >= 64 {
			break
		}
	}
	return cleaned.String()
}

// ActivityMiddleware records what the customer used — for their action log
// on the customer card and, aggregated with no customer in it, the button
// statistics — and when they were last active. It runs after the update is
// handled and never delays or fails it.
func (app *App) ActivityMiddleware() telegram.Middleware {
	return func(next telegram.HandlerFunc) telegram.HandlerFunc {
		return func(ctx context.Context, client *telegram.Bot, update *models.Update) {
			next(ctx, client, update)
			if app.customers == nil || update == nil {
				return
			}
			var telegramID int64
			switch {
			case update.CallbackQuery != nil:
				telegramID = update.CallbackQuery.From.ID
			case update.Message != nil && update.Message.From != nil:
				telegramID = update.Message.From.ID
			default:
				return
			}
			action := activityAction(update)
			recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			app.customers.recordActivity(recordCtx, telegramID, action)
		}
	}
}

// recordActivity touches last activity and logs the action, both best effort.
func (store *PostgresStore) recordActivity(ctx context.Context, telegramID int64, action string) {
	subject := strconv.FormatInt(telegramID, 10)
	var userID string
	if err := store.pool.QueryRow(ctx, `SELECT user_id::text FROM identities
		WHERE provider = 'telegram' AND provider_subject = $1 AND status = 'active'`, subject).Scan(&userID); err != nil {
		return
	}
	_, _ = store.pool.Exec(ctx, `UPDATE users SET last_activity_at = now()
		WHERE id = $1::uuid AND (last_activity_at IS NULL OR last_activity_at < now() - interval '5 minutes')`, userID)
	if action != "" {
		_, _ = store.pool.Exec(ctx, `INSERT INTO customer_activity (user_id, surface, action) VALUES ($1::uuid, 'bot', $2)`, userID, action)
	}
}
