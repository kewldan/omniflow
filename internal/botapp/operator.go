package botapp

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/backup"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// OperatorTools is the bot's operator surface: backup status, an on-demand
// backup, and a restore. Telegram carries operator notifications and
// backup/restore only; every other administrative control lives in the web
// panel.
type OperatorTools struct {
	backups   *backup.Service
	store     *PostgresStore
	operators map[int64]struct{}
}

// EnableOperatorTools registers the operator accounts allowed to run backup and
// restore actions. An empty list disables the surface entirely, which is the
// default: an installation must name its operators explicitly.
func (app *App) EnableOperatorTools(backups *backup.Service, operatorIDs []int64) {
	if backups == nil || len(operatorIDs) == 0 {
		return
	}
	allowed := make(map[int64]struct{}, len(operatorIDs))
	for _, id := range operatorIDs {
		allowed[id] = struct{}{}
	}
	app.operators = &OperatorTools{backups: backups, store: app.customers, operators: allowed}
}

// isOperator reports whether a Telegram account may run operator actions.
func (app *App) isOperator(telegramID int64) bool {
	if app.operators == nil {
		return false
	}
	_, allowed := app.operators.operators[telegramID]
	return allowed
}

// HandleOps renders the operator panel. It answers nothing at all to a
// non-operator, so the command's existence is not confirmed to a customer.
func (app *App) HandleOps(ctx context.Context, client *telegram.Bot, update *models.Update) {
	message := update.Message
	if message == nil || message.From == nil || message.Chat.Type != models.ChatTypePrivate {
		return
	}
	if !app.isOperator(message.From.ID) {
		return
	}
	view := app.operatorPanel(ctx)
	if _, err := client.SendMessage(ctx, sendParams(message.Chat.ID, view)); err != nil {
		app.logger.Error("operator panel delivery failed", "error", err)
	}
}

// operatorPanel summarises backup health. It shows sizes and timestamps only:
// no connection details, no file paths, and no encryption material.
func (app *App) operatorPanel(ctx context.Context) View {
	if app.operators == nil {
		return View{Text: "Operator tools are not configured."}
	}
	if !app.operators.backups.Enabled() {
		return View{Text: "💾 <b>Backups</b>\n\nBackups are disabled. Set <code>APP_BACKUP_ENABLED</code> and <code>APP_BACKUP_ENCRYPTION_KEY</code> to turn them on."}
	}
	records, err := app.operators.backups.List(ctx, 5)
	if err != nil {
		app.logger.Error("backup list failed", "error", err)
		return View{Text: "💾 <b>Backups</b>\n\nBackup status is unavailable right now."}
	}
	builder := &strings.Builder{}
	builder.WriteString("💾 <b>Backups</b>\n\n")
	if len(records) == 0 {
		builder.WriteString("No backup has been taken yet.")
	}
	for _, record := range records {
		builder.WriteString(operatorBackupLine(record))
	}
	buttons := [][]models.InlineKeyboardButton{
		row(actionButton("💾 Back up now", "ops-backup")),
	}
	if latest, found, latestErr := app.operators.backups.Latest(ctx); latestErr == nil && found {
		buttons = append(buttons, row(actionButton("♻️ Restore latest…", "ops-restore-confirm:"+uuidText(latest.ID))))
	}
	buttons = append(buttons, row(actionButton("🔄 Refresh", "ops")))
	return View{Text: builder.String(), Keyboard: keyboard(buttons...)}
}

func operatorBackupLine(record dbgen.Backup) string {
	stamp := record.StartedAt.Time.UTC().Format(time.RFC3339)
	status := record.Status
	if record.ErrorCode.Valid {
		status += " (" + record.ErrorCode.String + ")"
	}
	return fmt.Sprintf("• <code>%s</code>\n  %s · %s · %s\n",
		html.EscapeString(record.FileName), html.EscapeString(status),
		formatBytes(record.SizeBytes), html.EscapeString(stamp))
}

// handleOperatorAction runs a backup or restore action. Restores are two-step:
// the first tap only renders the confirmation, and the second one carries the
// backup identifier the operator saw.
func (app *App) handleOperatorAction(ctx context.Context, telegramID int64, parts []string) (View, bool) {
	if app.operators == nil || !app.isOperator(telegramID) {
		return View{}, false
	}
	switch parts[0] {
	case "ops":
		return app.operatorPanel(ctx), true
	case "ops-backup":
		record, err := app.operators.backups.Create(ctx, "manual", fmt.Sprintf("telegram:%d", telegramID))
		if errors.Is(err, backup.ErrBusy) {
			return View{Text: "💾 A backup is already running. Try again once it finishes.", Keyboard: keyboard(row(actionButton("🔄 Refresh", "ops")))}, true
		}
		if err != nil {
			app.logger.Error("manual backup failed", "error", err)
			return View{Text: "⚠️ The backup did not complete. Check the worker logs for the classified error code.", Keyboard: keyboard(row(actionButton("🔄 Refresh", "ops")))}, true
		}
		app.recordOperatorAudit(ctx, telegramID, "backup.created", uuidText(record.ID), "manual backup from Telegram")
		return app.operatorPanel(ctx), true
	case "ops-restore-confirm":
		if len(parts) != 2 {
			return app.operatorPanel(ctx), true
		}
		return View{
			Text: "♻️ <b>Restore this backup?</b>\n\nThis replaces the current database contents with the backup. Every change made since it was taken is lost.\n\nBackup: <code>" + html.EscapeString(parts[1]) + "</code>",
			Keyboard: keyboard(
				row(actionButton("Yes, restore now", "ops-restore:"+parts[1])),
				row(actionButton("Cancel", "ops")),
			),
		}, true
	case "ops-restore":
		if len(parts) != 2 {
			return app.operatorPanel(ctx), true
		}
		reason := fmt.Sprintf("restore requested from Telegram by operator %d", telegramID)
		app.recordOperatorAudit(ctx, telegramID, "backup.restore_requested", parts[1], reason)
		restore, err := app.operators.backups.Restore(ctx, parts[1], fmt.Sprintf("telegram:%d", telegramID), reason)
		if err != nil {
			app.logger.Error("restore failed", "error", err)
			return View{Text: "⚠️ The restore did not complete. The database was left as it was found; check the worker logs.", Keyboard: keyboard(row(actionButton("🔄 Refresh", "ops")))}, true
		}
		app.recordOperatorAudit(ctx, telegramID, "backup.restored", parts[1], "restore completed with status "+restore.Status)
		return View{Text: "✅ <b>Restore completed</b>\n\nStatus: <code>" + html.EscapeString(restore.Status) + "</code>", Keyboard: keyboard(row(actionButton("🔄 Refresh", "ops")))}, true
	default:
		return View{}, false
	}
}

// recordOperatorAudit appends the append-only audit event for an operator
// action. The actor is the Telegram account, never a customer identifier.
func (app *App) recordOperatorAudit(ctx context.Context, telegramID int64, action, targetID, reason string) {
	if app.customers == nil {
		return
	}
	if err := app.customers.RecordAuditEvent(ctx, fmt.Sprintf("telegram:%d", telegramID), action, "backup", targetID, reason); err != nil {
		app.logger.Error("operator audit event could not be recorded", "action", action, "error", err)
	}
}
