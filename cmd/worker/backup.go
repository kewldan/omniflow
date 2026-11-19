package main

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/omniflow/omniflow/internal/app"
	"github.com/omniflow/omniflow/internal/backup"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/installsettings"
	"github.com/omniflow/omniflow/internal/jobs"
	"github.com/omniflow/omniflow/internal/operator"
)

// scheduledBackup is the backup pass. River runs it every five minutes; it
// asks the backup section of the installation settings whether a backup is
// due — on an interval, or at a time of day — and claims the run through the
// gate, so a schedule changed in the panel applies on the next tick.
type scheduledBackup struct {
	service   *backup.Service
	settings  *installsettings.Reader
	gate      *jobs.Gate
	logger    *slog.Logger
	fallback  config.BackupConfig
	telegram  string
	transport config.TelegramTransportConfig
}

func (scheduled scheduledBackup) Run(ctx context.Context) error {
	section := scheduled.settings.Section(ctx, "backup")
	if !section.Bool("enabled", scheduled.fallback.Enabled) {
		return nil
	}
	scheduled.service.SetRetention(section.Duration("retentionDays", 24*time.Hour, scheduled.fallback.Retention))
	var (
		due bool
		err error
	)
	if slot, ok := installsettings.ClockMinutes(section.String("timeOfDay", "")); ok {
		due, err = scheduled.gate.ClaimAt(ctx, "backup", time.Now().In(scheduled.settings.Location(ctx)), slot)
	} else {
		due, err = scheduled.gate.Claim(ctx, "backup",
			section.Duration("intervalHours", time.Hour, scheduled.fallback.Interval))
	}
	if err != nil || !due {
		return err
	}
	record, err := scheduled.service.Create(ctx, "scheduled", "scheduler")
	if err != nil && !errors.Is(err, backup.ErrBusy) {
		return err
	}
	if err == nil {
		scheduled.deliver(ctx, section, record.FileName, scheduled.service.Path(record))
	}
	return scheduled.service.Prune(ctx)
}

// deliver sends the encrypted file to the chat the backup section names. The
// file is useless without APP_BACKUP_ENCRYPTION_KEY, which never leaves the
// installation, so a copy in Telegram is an off-site backup rather than a
// disclosure. A failed send is logged and never fails the backup.
func (scheduled scheduledBackup) deliver(ctx context.Context, section installsettings.Document, name, path string) {
	raw := section.String("telegramChatId", "")
	if raw == "" || scheduled.telegram == "" {
		return
	}
	chat, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || chat == 0 {
		scheduled.logger.Warn("backup chat is not a Telegram chat identifier")
		return
	}
	thread := section.Int("telegramThreadId", 0)
	if err = operator.SendBackupFile(ctx, scheduled.telegram, app.TelegramOptions(scheduled.transport),
		chat, thread, name, path); err != nil {
		scheduled.logger.Warn("backup file could not be sent to Telegram", "error", err)
	}
}
