package botapp

import (
	"context"
	"log/slog"
	"strings"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// defaultNamePatterns are what a display name may not contain when the
// moderation section's filter is on and names no patterns of its own: the
// words an impersonator of the service's staff uses, and links.
var defaultNamePatterns = []string{
	"admin", "support", "moderator", "админ", "поддержк", "модератор", "t.me/", "http://", "https://", "@",
}

// senderOf returns the user behind an update.
func senderOf(update *models.Update) *models.User {
	switch {
	case update == nil:
		return nil
	case update.Message != nil:
		return update.Message.From
	case update.CallbackQuery != nil:
		return &update.CallbackQuery.From
	}
	return nil
}

// dangerousName reports whether a display name or username contains a
// forbidden pattern. Matching is case-insensitive over the name as shown.
func dangerousName(user *models.User, patterns []string) bool {
	shown := strings.ToLower(strings.Join([]string{user.FirstName, user.LastName, user.Username}, " "))
	for _, pattern := range patterns {
		if pattern = strings.ToLower(strings.TrimSpace(pattern)); pattern != "" && strings.Contains(shown, pattern) {
			return true
		}
	}
	return false
}

// NameFilterMiddleware refuses service to an account whose display name
// pretends to be staff or carries a link, when the moderation section turns
// the filter on: such an account opening a support ticket or a chat with a
// customer is how impersonation scams start. Operators are exempt. The
// customer is told why and what to change, and nothing else happens.
func (app *App) NameFilterMiddleware() telegram.Middleware {
	return func(next telegram.HandlerFunc) telegram.HandlerFunc {
		return func(ctx context.Context, client *telegram.Bot, update *models.Update) {
			moderation := app.section("moderation")
			user := senderOf(update)
			if user == nil || !moderation.Bool("filterDisplayNames", false) || app.isOperator(user.ID) {
				next(ctx, client, update)
				return
			}
			patterns := moderation.Strings("blockedNamePatterns", nil)
			if len(patterns) == 0 {
				patterns = defaultNamePatterns
			}
			if !dangerousName(user, patterns) {
				next(ctx, client, update)
				return
			}
			locale := localeFrom(user.LanguageCode)
			if update.CallbackQuery != nil {
				_, _ = client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{
					CallbackQueryID: update.CallbackQuery.ID, Text: text(locale, "moderation.nameRefused"), ShowAlert: true,
				})
				return
			}
			_, _ = client.SendMessage(ctx, &telegram.SendMessageParams{ChatID: user.ID, Text: text(locale, "moderation.nameRefused")})
		}
	}
}

// BlockerScanner runs the blocker scans operators request in the panel. It
// lives in the bot because only the bot holds the Telegram client: a chat
// action is the cheapest request that tells a blocked chat from a live one,
// and it shows the customer nothing but a moment of "typing".
type BlockerScanner struct {
	pool   *pgxpool.Pool
	store  *PostgresStore
	client *telegram.Bot
	logger *slog.Logger
	// pace is the gap between two checks: about twenty a second, under the
	// Bot API's broadcast ceiling.
	pace time.Duration
}

// NewBlockerScanner builds the scanner.
func NewBlockerScanner(pool *pgxpool.Pool, store *PostgresStore, client *telegram.Bot, logger *slog.Logger) *BlockerScanner {
	return &BlockerScanner{pool: pool, store: store, client: client, logger: logger, pace: 50 * time.Millisecond}
}

// Run looks for a requested scan every thirty seconds.
func (scanner *BlockerScanner) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		scanner.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce claims a pending scan, if any, and checks every Telegram customer.
func (scanner *BlockerScanner) RunOnce(ctx context.Context) {
	queries := dbgen.New(scanner.pool)
	scan, err := queries.ClaimBlockerScan(ctx)
	if err != nil {
		return
	}
	rows, err := scanner.pool.Query(ctx, telegramRecipients+` SELECT DISTINCT ON (telegram_id) telegram_id FROM recipient ORDER BY telegram_id`)
	if err != nil {
		_ = queries.FinishBlockerScan(ctx, dbgen.FinishBlockerScanParams{ScanID: scan.ID, Status: "failed"})
		return
	}
	var telegramIDs []int64
	for rows.Next() {
		var telegramID int64
		if rows.Scan(&telegramID) == nil {
			telegramIDs = append(telegramIDs, telegramID)
		}
	}
	rows.Close()
	checked, blocked := int32(0), int32(0)
	for _, telegramID := range telegramIDs {
		if ctx.Err() != nil {
			_ = queries.FinishBlockerScan(context.WithoutCancel(ctx), dbgen.FinishBlockerScanParams{ScanID: scan.ID, Status: "failed"})
			return
		}
		_, sendErr := scanner.client.SendChatAction(ctx, &telegram.SendChatActionParams{ChatID: telegramID, Action: models.ChatActionTyping})
		checked++
		if sendErr == nil {
			_ = scanner.store.RecordReachable(ctx, telegramID)
		} else if chatClosed(sendErr) {
			blocked++
			_ = scanner.store.RecordBlockedChat(ctx, telegramID)
		}
		if checked%100 == 0 {
			_ = queries.ProgressBlockerScan(ctx, dbgen.ProgressBlockerScanParams{ScanID: scan.ID, Checked: checked, Blocked: blocked})
		}
		time.Sleep(scanner.pace)
	}
	_ = queries.ProgressBlockerScan(ctx, dbgen.ProgressBlockerScanParams{ScanID: scan.ID, Checked: checked, Blocked: blocked})
	_ = queries.FinishBlockerScan(ctx, dbgen.FinishBlockerScanParams{ScanID: scan.ID, Status: "completed"})
	scanner.logger.Info("blocker scan finished", "checked", checked, "blocked", blocked)
}

// chatClosed reports a send refused because the customer blocked the bot,
// deleted their account, or never started it.
func chatClosed(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "blocked") || strings.Contains(message, "deactivated") ||
		strings.Contains(message, "chat not found") || strings.Contains(message, "forbidden")
}
