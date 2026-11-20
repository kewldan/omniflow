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
// due — at a time of day, on an interval, or both — and claims each schedule
// through the gate, so a schedule changed in the panel applies on the next
// tick.
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
	due, err := scheduled.due(ctx, section)
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

// due claims whichever schedules are due. The time of day is local to the
// installation's timezone. The interval runs beside it when the operator set
// one; with only a time of day saved, the environment's interval
// (APP_BACKUP_INTERVAL_HOURS) does not also apply, so choosing "every day at
// 03:00" does not quietly add a second daily backup at some other hour. Each
// schedule has its own gate row, and both are claimed on every tick, so a
// tick on which both are due takes one backup and moves both on.
func (scheduled scheduledBackup) due(ctx context.Context, section installsettings.Document) (bool, error) {
	slot, daily, interval := backupSchedules(section)
	var due bool
	if daily {
		claimed, err := scheduled.gate.ClaimAt(ctx, "backup_daily",
			time.Now().In(scheduled.settings.Location(ctx)), slot)
		if err != nil {
			return false, err
		}
		due = claimed
	}
	if interval {
		claimed, err := scheduled.gate.Claim(ctx, "backup",
			section.Duration("intervalHours", time.Hour, scheduled.fallback.Interval))
		if err != nil {
			return false, err
		}
		due = due || claimed
	}
	return due, nil
}

// backupSchedules reads which schedules the backup section asks for: the
// minutes past local midnight of a daily backup, whether there is one, and
// whether the interval applies too.
func backupSchedules(section installsettings.Document) (slot int, daily, interval bool) {
	slot, daily = installsettings.ClockMinutes(section.String("timeOfDay", ""))
	return slot, daily, !daily || section.Int("intervalHours", 0) > 0
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
