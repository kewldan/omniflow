package botapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omniflow/omniflow/internal/accountsupport"
)

const (
	// attachmentCopyInterval is how often the bot looks for files to copy.
	attachmentCopyInterval = 30 * time.Second
	// attachmentCopyBatch bounds one pass.
	attachmentCopyBatch = 20
	// attachmentCopyAttempts is how many times a file is tried before it is
	// left as a Telegram reference only.
	attachmentCopyAttempts = 5
	// attachmentCopyMaxBytes is the schema's limit on an attachment.
	attachmentCopyMaxBytes = 10 << 20
)

// AttachmentCopier copies the files customers send the bot into the
// installation's attachment store, so the operator desk can show them. The
// bot is the one process that holds the token and receives the file, so the
// copy is made here, once, rather than fetched from Telegram on an operator's
// request; the desk then reads the copy like any web upload.
type AttachmentCopier struct {
	pool   *pgxpool.Pool
	bot    *telegram.Bot
	store  accountsupport.AttachmentStore
	client *http.Client
	logger *slog.Logger
}

// NewAttachmentCopier builds the copier. proxyURL is the Telegram proxy, when
// the installation routes Telegram through one.
func NewAttachmentCopier(pool *pgxpool.Pool, bot *telegram.Bot, store accountsupport.AttachmentStore,
	proxyURL string, logger *slog.Logger,
) *AttachmentCopier {
	client := &http.Client{Timeout: 60 * time.Second}
	if proxyURL != "" {
		if proxy, err := url.Parse(proxyURL); err == nil {
			client.Transport = &http.Transport{Proxy: http.ProxyURL(proxy), ForceAttemptHTTP2: true}
		}
	}
	return &AttachmentCopier{pool: pool, bot: bot, store: store, client: client, logger: logger}
}

// Run copies pending files until the context ends.
func (copier *AttachmentCopier) Run(ctx context.Context) {
	ticker := time.NewTicker(attachmentCopyInterval)
	defer ticker.Stop()
	for {
		copier.CopyPending(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type pendingAttachment struct {
	id     string
	fileID string
}

// CopyPending copies one batch of files not yet in the store.
func (copier *AttachmentCopier) CopyPending(ctx context.Context) {
	rows, err := copier.pool.Query(ctx, `SELECT id::text, telegram_file_id
		FROM support_attachments
		WHERE origin = 'telegram' AND storage_key IS NULL
		  AND fetch_attempts < $1 AND retain_until > now()
		ORDER BY created_at LIMIT $2`, attachmentCopyAttempts, attachmentCopyBatch)
	if err != nil {
		copier.logger.Warn("support attachment copy lookup failed", "error", err)
		return
	}
	var pending []pendingAttachment
	for rows.Next() {
		var item pendingAttachment
		if err := rows.Scan(&item.id, &item.fileID); err != nil {
			rows.Close()
			copier.logger.Warn("support attachment copy lookup failed", "error", err)
			return
		}
		pending = append(pending, item)
	}
	rows.Close()
	for _, item := range pending {
		if ctx.Err() != nil {
			return
		}
		copier.copy(ctx, item)
	}
}

func (copier *AttachmentCopier) copy(ctx context.Context, item pendingAttachment) {
	content, err := copier.download(ctx, item.fileID)
	if err == nil {
		key := accountsupport.ContentKey(content)
		if err = copier.store.Put(ctx, key, content); err == nil {
			_, err = copier.pool.Exec(ctx, `UPDATE support_attachments SET storage_key = $2
				WHERE id = $1::uuid AND storage_key IS NULL`, item.id, key)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				// The same message already holds a copy of the same bytes; this
				// row stays a reference and is not tried again.
				_, err = copier.pool.Exec(ctx, `UPDATE support_attachments SET fetch_attempts = $2
					WHERE id = $1::uuid`, item.id, attachmentCopyAttempts)
			}
			if err == nil {
				return
			}
		}
	}
	copier.logger.Warn("support attachment copy failed", "attachment", item.id, "error", err)
	if _, markErr := copier.pool.Exec(ctx, `UPDATE support_attachments SET fetch_attempts = fetch_attempts + 1
		WHERE id = $1::uuid`, item.id); markErr != nil {
		copier.logger.Warn("support attachment copy bookkeeping failed", "error", markErr)
	}
}

func (copier *AttachmentCopier) download(ctx context.Context, fileID string) ([]byte, error) {
	file, err := copier.bot.GetFile(ctx, &telegram.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("telegram file: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, copier.bot.FileDownloadLink(file), nil)
	if err != nil {
		return nil, errors.New("telegram file download could not be prepared")
	}
	response, err := copier.client.Do(request)
	if err != nil {
		// The download link carries the bot token; the error is reported
		// without it.
		return nil, errors.New("telegram file download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram file download answered %d", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, attachmentCopyMaxBytes+1))
	if err != nil {
		return nil, errors.New("telegram file download failed")
	}
	if len(content) == 0 || len(content) > attachmentCopyMaxBytes {
		return nil, errors.New("telegram file is empty or larger than the attachment limit")
	}
	return content, nil
}
