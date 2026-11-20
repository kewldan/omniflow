package operator

import (
	"context"
	"errors"
	"fmt"
	"os"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// The largest document the public Bot API accepts from a bot, and the one a
// self-hosted Bot API server (APP_TELEGRAM_API_URL) accepts. The check stays
// either way, because a refusal after uploading half a gigabyte is the worse
// failure.
const (
	telegramFileLimit           = 50 << 20
	selfHostedTelegramFileLimit = 2000 << 20
)

// BackupFileLimit is the size SendBackupFile will upload: 50 MB through the
// public Bot API, 2 GB through a self-hosted one.
func BackupFileLimit(selfHosted bool) int64 {
	if selfHosted {
		return selfHostedTelegramFileLimit
	}
	return telegramFileLimit
}

// ErrBackupTooLarge refuses a file the Bot API would reject.
var ErrBackupTooLarge = errors.New("backup file exceeds the Telegram upload limit")

// SendBackupFile uploads an encrypted backup to a chat (and topic) as a
// document. The caption names the file and nothing else.
func SendBackupFile(
	ctx context.Context, token string, options []telegram.Option, limit int64, chat int64, thread int, name, path string,
) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if limit <= 0 {
		limit = telegramFileLimit
	}
	if info.Size() > limit {
		return fmt.Errorf("%w: %d bytes", ErrBackupTooLarge, info.Size())
	}
	client, err := telegram.New(token, append([]telegram.Option{telegram.WithSkipGetMe()}, options...)...)
	if err != nil {
		return err
	}
	_, err = client.SendDocument(ctx, &telegram.SendDocumentParams{
		ChatID: chat, MessageThreadID: thread,
		Document: &models.InputFileUpload{Filename: name, Data: file},
		Caption:  "💾 " + name,
	})
	return err
}
