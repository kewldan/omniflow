package accountsupport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// defaultAttachmentDirectory follows the convention the backup directory already
// set: a fixed path under /var/lib/omniflow that a deployment mounts a volume
// at. It is not a temporary directory, because an attachment has to survive a
// restart for as long as the ticket that carries it.
const defaultAttachmentDirectory = "/var/lib/omniflow/support-attachments"

// originLocal marks an attachment whose bytes this installation holds.
//
// `support_attachments` was designed for Telegram, where the file lives in
// Telegram and Omniflow stores only the identifier that fetches it. A browser
// upload has no such custodian, so the bytes are written to the attachment
// directory and `storage_key` carries the content-addressed key that finds them.
//
// `origin` is what keeps the two kinds apart, and the table enforces it:
// exactly one of the two references is present, and it is the one the origin
// names. Anything sending a stored attachment back to Telegram must skip a local
// row — a content digest means nothing to Telegram — and this package refuses to
// serve a Telegram row, because it does not hold those bytes. The bot only ever
// describes an attachment by name and size, so a web upload renders correctly in
// a chat without either side special-casing the other.
const originLocal = "local"

// Attachment is the metadata a customer may see about one file.
//
// There is no field for a path, a bucket, or a Telegram identifier. Those are
// storage detail, and a customer response that carried one would be handing out
// a way to reach the file that does not pass through the ownership check.
type Attachment struct {
	ID        string
	MessageID int64
	Kind      string
	FileName  string
	MediaType string
	SizeBytes int64
	CreatedAt time.Time
	// Downloadable is false for a file that arrived through Telegram, whose bytes
	// Omniflow never held. The panel needs to know before it renders a download
	// control that will only ever explain itself.
	Downloadable bool
}

// NewAttachment is one multipart upload.
type NewAttachment struct {
	CustomerID string
	TicketID   string
	// Body is the optional text sent alongside the file. An upload is a message
	// like any other, so it takes the same path a reply does — including the rule
	// that a resolved ticket reopens when the customer writes into it.
	Body      string
	FileName  string
	MediaType string
	Content   []byte
}

// AttachmentStore holds the bytes of an upload that arrived through the web.
//
// It is an interface so an installation that grows an object store later can
// supply one without this package learning about buckets, and so tests can
// exercise the upload rules without touching a disk.
type AttachmentStore interface {
	// Put writes content under a key. It must be idempotent: the key is a digest
	// of the content, so writing the same key twice is writing the same bytes.
	Put(ctx context.Context, key string, content []byte) error
	// Open reads content back. A missing key must report os.ErrNotExist so the
	// caller can tell "gone" apart from "broken".
	Open(ctx context.Context, key string) ([]byte, error)
}

// ContentKey is the storage key for a file: the hex SHA-256 of its bytes.
//
// Content addressing is what makes Put idempotent and what lets an identical
// file uploaded twice occupy one entry. It is also why retention cannot delete a
// file just because one row expired — several rows may name the same key.
func ContentKey(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

// DirectoryStore keeps attachments as files under Root.
//
// Files are named by their content digest and fanned out one level, so a
// directory listing stays workable and an identical file uploaded twice occupies
// one entry. Nothing in the name comes from the customer: a supplied file name
// is display text, and letting it choose a path is how an upload becomes a way
// to write anywhere on the disk.
type DirectoryStore struct {
	Root string
}

func (store DirectoryStore) path(key string) (string, error) {
	// The key is produced by this package as hex, never by a caller, but it is
	// still checked before it becomes a path. A store that trusts its key is one
	// refactor away from accepting one that traverses out of Root.
	if len(key) != 64 {
		return "", ErrAttachmentStorage
	}
	for _, character := range key {
		isDigit := character >= '0' && character <= '9'
		isHex := character >= 'a' && character <= 'f'
		if !isDigit && !isHex {
			return "", ErrAttachmentStorage
		}
	}
	return filepath.Join(store.Root, key[:2], key), nil
}

// Put writes the content, creating the directory tree on first use.
func (store DirectoryStore) Put(ctx context.Context, key string, content []byte) error {
	target, err := store.path(key)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("%w: %s", ErrAttachmentStorage, err)
	}
	// A file already present is the same file: the name is its digest. Rewriting
	// it would be correct but pointless, and skipping keeps a retried upload from
	// rewriting bytes a download may be reading.
	if _, err = os.Stat(target); err == nil {
		return nil
	}
	// The write lands on a temporary name first, so a crash mid-write cannot
	// leave a truncated file under a digest that promises complete content.
	temporary := target + ".partial"
	if err = os.WriteFile(temporary, content, 0o600); err != nil {
		return fmt.Errorf("%w: %s", ErrAttachmentStorage, err)
	}
	if err = os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("%w: %s", ErrAttachmentStorage, err)
	}
	return nil
}

// Open reads the content back.
func (store DirectoryStore) Open(ctx context.Context, key string) ([]byte, error) {
	target, err := store.path(key)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrAttachmentStorage, err)
	}
	return content, nil
}

// Attach stores one uploaded file as a new message on the conversation.
func (service *Service) Attach(ctx context.Context, input NewAttachment) (Attachment, error) {
	accepted, err := service.limits.Accept(input.FileName, input.MediaType, int64(len(input.Content)))
	if err != nil {
		return Attachment{}, err
	}
	if !looksLikeUUID(strings.TrimSpace(input.TicketID)) {
		return Attachment{}, ErrNotFound
	}
	body := strings.TrimSpace(input.Body)
	if len([]rune(body)) > MaxMessageLength {
		return Attachment{}, invalid("a support message can hold at most 4000 characters")
	}
	if body == "" {
		// `support_messages.body` cannot be empty, and the bot writes the same
		// placeholder for a file sent with no caption. Matching it keeps one
		// conversation reading the same way on both surfaces.
		body = "[attachment]"
	}

	// Ownership is checked before a single byte is written. The transaction below
	// checks it again under a lock and is the authoritative one, but without this
	// first pass a stranger could spend the installation's disk on a ticket they
	// cannot see — the row would be refused and the file would already be there.
	if err = service.ownsTicket(ctx, input.CustomerID, input.TicketID); err != nil {
		return Attachment{}, err
	}

	// The bytes are written before the row, so a row can never point at a file
	// that is not there. The reverse order would produce an attachment the panel
	// offers and the download route cannot serve.
	key := ContentKey(input.Content)
	if err = service.attachments.Put(ctx, key, input.Content); err != nil {
		service.logger.Error("support attachment could not be stored", "error", err)
		return Attachment{}, ErrAttachmentStorage
	}

	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return Attachment{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockWritableTicket(ctx, tx, input.CustomerID, input.TicketID); err != nil {
		return Attachment{}, err
	}
	messageID, err := appendCustomerMessage(ctx, tx, input.TicketID, body, "")
	if err != nil {
		return Attachment{}, err
	}
	attachment := Attachment{
		MessageID: messageID, Kind: accepted.Kind, FileName: accepted.FileName,
		MediaType: accepted.MediaType, SizeBytes: accepted.SizeBytes, Downloadable: true,
	}
	if err = tx.QueryRow(ctx, `INSERT INTO support_attachments
		(message_id, kind, origin, storage_key, file_name, mime_type, size_bytes)
		VALUES ($1, $2, 'local', $3, NULLIF($4, ''), NULLIF($5, ''), $6)
		RETURNING id::text, created_at`,
		messageID, accepted.Kind, key, accepted.FileName,
		accepted.MediaType, accepted.SizeBytes).
		Scan(&attachment.ID, &attachment.CreatedAt); err != nil {
		return Attachment{}, err
	}
	attachment.CreatedAt = attachment.CreatedAt.UTC()
	return attachment, tx.Commit(ctx)
}

// Attachment serves one file to the customer who owns the ticket it hangs on.
//
// Ownership is joined in the query rather than checked afterwards, so there is
// no path through this function that reads bytes before it knows whose they are.
func (service *Service) Attachment(
	ctx context.Context, customerID, attachmentID string,
) (Attachment, []byte, error) {
	if !looksLikeUUID(strings.TrimSpace(attachmentID)) {
		return Attachment{}, nil, ErrNotFound
	}
	var attachment Attachment
	var origin, key string
	err := service.pool.QueryRow(ctx, `SELECT a.id::text, a.message_id, a.kind,
		COALESCE(a.file_name, ''), COALESCE(a.mime_type, ''), a.size_bytes,
		a.created_at, a.origin, COALESCE(a.storage_key, '')
		FROM support_attachments a
		JOIN support_messages m ON m.id = a.message_id
		JOIN support_tickets t ON t.id = m.ticket_id
		WHERE a.id = $2::uuid AND t.user_id = $1::uuid AND a.retain_until > now()`,
		customerID, attachmentID).
		Scan(&attachment.ID, &attachment.MessageID, &attachment.Kind, &attachment.FileName,
			&attachment.MediaType, &attachment.SizeBytes, &attachment.CreatedAt, &origin, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attachment{}, nil, ErrNotFound
	}
	if err != nil {
		return Attachment{}, nil, err
	}
	attachment.CreatedAt = attachment.CreatedAt.UTC()
	if origin != originLocal {
		return attachment, nil, ErrAttachmentRemote
	}
	attachment.Downloadable = true
	content, err := service.attachments.Open(ctx, key)
	if errors.Is(err, os.ErrNotExist) {
		// The row outlived its file. Retention removes attachment rows on their
		// own schedule and nothing sweeps the directory alongside them, so this is
		// a reachable state rather than an impossible one, and it reads to the
		// customer as a file that is no longer there.
		return attachment, nil, ErrNotFound
	}
	if err != nil {
		service.logger.Error("support attachment could not be read", "error", err)
		return attachment, nil, ErrAttachmentStorage
	}
	return attachment, content, nil
}

// attachMessageFiles fills in the attachment metadata for a page of messages
// with one query rather than one per message.
func (service *Service) attachMessageFiles(ctx context.Context, messages []Message) error {
	if len(messages) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	rows, err := service.pool.Query(ctx, `SELECT a.id::text, a.message_id, a.kind,
		COALESCE(a.file_name, ''), COALESCE(a.mime_type, ''), a.size_bytes,
		a.created_at, a.origin
		FROM support_attachments a
		WHERE a.message_id = ANY($1) AND a.retain_until > now()
		ORDER BY a.created_at, a.id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	byMessage := make(map[int64][]Attachment, len(messages))
	for rows.Next() {
		var attachment Attachment
		var origin string
		if err = rows.Scan(&attachment.ID, &attachment.MessageID, &attachment.Kind,
			&attachment.FileName, &attachment.MediaType, &attachment.SizeBytes,
			&attachment.CreatedAt, &origin); err != nil {
			return err
		}
		attachment.CreatedAt = attachment.CreatedAt.UTC()
		attachment.Downloadable = origin == originLocal
		byMessage[attachment.MessageID] = append(byMessage[attachment.MessageID], attachment)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for index := range messages {
		messages[index].Attachments = byMessage[messages[index].ID]
	}
	return nil
}
