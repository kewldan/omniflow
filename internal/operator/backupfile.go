package operator

import (
	"context"
	"errors"
	"fmt"
	"os"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// telegramFileLimit is the largest document the public Bot API accepts from a
// bot. A self-hosted Bot API server lifts it; the check stays, because a
// refusal after uploading half a gigabyte is the worse failure.
const telegramFileLimit = 50 << 20

// ErrBackupTooLarge refuses a file the Bot API would reject.
var ErrBackupTooLarge = errors.New("backup file exceeds the Telegram upload limit")

// SendBackupFile uploads an encrypted backup to a chat (and topic) as a
// document. The caption names the file and nothing else.
func SendBackupFile(
	ctx context.Context, token string, options []telegram.Option, chat int64, thread int, name, path string,
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
	if info.Size() > telegramFileLimit {
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
