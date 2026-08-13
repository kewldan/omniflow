//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/accountsupport"
	"github.com/omniflow/omniflow/internal/retention"
)

// Attachment files are named by their content digest, so two customers who
// upload identical bytes share one file. Retention must therefore reclaim a file
// only once no surviving row references it — deleting on the strength of a
// single expired row would break the other customer's download while leaving
// their row claiming the file is there.
func TestRetentionReclaimsOnlyUnreferencedAttachmentFiles(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	root := t.TempDir()

	support, err := accountsupport.New(harness.pool, accountsupport.Options{
		Logger: slog.New(slog.DiscardHandler), AttachmentDirectory: root,
	})
	if err != nil {
		t.Fatalf("build support service: %v", err)
	}

	// Two rows over one digest, and a third over a digest of its own. Only the
	// third's file may be reclaimed when every row expires except the survivor.
	shared := writeAttachmentFile(t, root, []byte("shared bytes"))
	lonely := writeAttachmentFile(t, root, []byte("lonely bytes"))

	expiredShared := seedAttachmentRow(t, ctx, harness, shared, true)
	survivingShared := seedAttachmentRow(t, ctx, harness, shared, false)
	expiredLonely := seedAttachmentRow(t, ctx, harness, lonely, true)

	rows, files, err := support.SweepExpiredAttachments(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if rows != 2 {
		t.Fatalf("deleted %d rows, want 2", rows)
	}
	if files != 1 {
		t.Fatalf("reclaimed %d files, want 1 — only the unreferenced digest", files)
	}

	if fileExists(t, root, shared) != true {
		t.Fatal("the shared file was removed while a row still references it")
	}
	if fileExists(t, root, lonely) != false {
		t.Fatal("the unreferenced file was left on disk")
	}
	assertAttachmentRow(t, ctx, harness, expiredShared, false)
	assertAttachmentRow(t, ctx, harness, expiredLonely, false)
	assertAttachmentRow(t, ctx, harness, survivingShared, true)

	// A second pass has nothing to do. Retention runs hourly, so a sweep that
	// failed on an already-reclaimed file would fail forever.
	rows, files, err = support.SweepExpiredAttachments(ctx)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if rows != 0 || files != 0 {
		t.Fatalf("second sweep removed %d rows and %d files, want 0 and 0", rows, files)
	}
}

// The worker still deletes rows when no sweeper is attached, which is what a
// Telegram-only installation runs: those attachments reference bytes Telegram
// holds and there is nothing local to reclaim.
func TestRetentionStillDeletesAttachmentRowsWithoutASweeper(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	root := t.TempDir()

	expired := seedAttachmentRow(t, ctx, harness, writeAttachmentFile(t, root, []byte("x")), true)

	result := retention.New(harness.pool, slog.New(slog.DiscardHandler), retention.Config{}).Sweep(ctx)
	if result.Attachments == 0 {
		t.Fatal("no attachment rows were removed")
	}
	assertAttachmentRow(t, ctx, harness, expired, false)
}

// An attachment's origin and its reference have to agree, and the table is what
// makes them. Before `origin` and `storage_key` existed, a local upload wrote its
// key into `telegram_file_id` behind a `web:` prefix, and "is this file ours" was
// a string test every reader had to remember to perform. A reader that forgot
// would have handed a Telegram identifier to a file reader, or a content digest
// to Telegram.
//
// These are the four rows the schema must refuse. None of them is reachable
// through the Go paths today, which is the point: the guarantee should not
// depend on every future caller getting it right.
func TestSupportAttachmentsRefuseAMismatchedReference(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	messageID := seedAttachmentMessage(t, ctx, harness)
	digest := accountsupport.ContentKey([]byte("some bytes"))

	cases := []struct {
		name       string
		columns    string
		values     string
		args       []any
		constraint string
	}{
		{
			name:       "local row carrying a Telegram identifier",
			columns:    "origin, storage_key, telegram_file_id",
			values:     "'local', $2, 'AgACAgIAAxkBAAI'",
			args:       []any{digest},
			constraint: "support_attachments_reference_matches_origin",
		},
		{
			name:       "telegram row with nothing to fetch",
			columns:    "origin",
			values:     "'telegram'",
			constraint: "support_attachments_reference_matches_origin",
		},
		{
			name:       "local row with no bytes to point at",
			columns:    "origin",
			values:     "'local'",
			constraint: "support_attachments_reference_matches_origin",
		},
		{
			// The key names a file on disk, so a value that is not a digest is a
			// value that could name something else entirely.
			name:       "storage key that is not a content digest",
			columns:    "origin, storage_key",
			values:     "'local', $2",
			args:       []any{"../../etc/passwd"},
			constraint: "support_attachments_storage_key_shape",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			args := append([]any{messageID}, testCase.args...)
			_, err := harness.pool.Exec(ctx, `INSERT INTO support_attachments
				(message_id, kind, size_bytes, `+testCase.columns+`)
				VALUES ($1, 'document', 12, `+testCase.values+`)`, args...)
			if err == nil {
				t.Fatal("the row was accepted")
			}
			if !strings.Contains(err.Error(), testCase.constraint) {
				t.Fatalf("refused by something other than %s: %v", testCase.constraint, err)
			}
		})
	}
}

// One message never carries the same stored file twice. `UNIQUE (message_id,
// telegram_file_id)` gave Telegram attachments that property and stops applying
// to local rows once their reference moves to a nullable column of its own,
// because NULLs are distinct.
func TestSupportAttachmentsRefuseTheSameStoredFileTwice(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	messageID := seedAttachmentMessage(t, ctx, harness)
	digest := accountsupport.ContentKey([]byte("identical bytes"))

	insert := func() error {
		_, err := harness.pool.Exec(ctx, `INSERT INTO support_attachments
			(message_id, kind, origin, storage_key, size_bytes)
			VALUES ($1, 'document', 'local', $2, 12)`, messageID, digest)
		return err
	}
	if err := insert(); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err := insert()
	if err == nil {
		t.Fatal("the same stored file was accepted twice on one message")
	}
	if !strings.Contains(err.Error(), "support_attachments_local_file_idx") {
		t.Fatalf("refused by something other than the local-file index: %v", err)
	}
}

// seedAttachmentMessage writes a customer, a ticket, and a message, and returns
// the message an attachment can hang on.
func seedAttachmentMessage(t *testing.T, ctx context.Context, harness *harness) int64 {
	t.Helper()
	customerID := seedAttachmentCustomer(t, ctx, harness)
	var ticketID string
	err := harness.pool.QueryRow(ctx, `INSERT INTO support_tickets (user_id, queue_id, subject, status)
		VALUES ($1::uuid, (SELECT id FROM support_queues WHERE code = 'general'), 'constraints', 'open')
		RETURNING id::text`, customerID).Scan(&ticketID)
	if err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	var messageID int64
	err = harness.pool.QueryRow(ctx, `INSERT INTO support_messages (ticket_id, sender, body)
		VALUES ($1::uuid, 'customer', '[attachment]') RETURNING id`, ticketID).Scan(&messageID)
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}
	return messageID
}

// writeAttachmentFile stores content through a DirectoryStore and returns the
// digest key the row will carry.
func writeAttachmentFile(t *testing.T, root string, content []byte) string {
	t.Helper()
	digest := accountsupport.ContentKey(content)
	if err := (accountsupport.DirectoryStore{Root: root}).Put(context.Background(), digest, content); err != nil {
		t.Fatalf("store attachment: %v", err)
	}
	return digest
}

func fileExists(t *testing.T, root, digest string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, digest[:2], digest))
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	t.Fatalf("stat attachment: %v", err)
	return false
}

func seedAttachmentCustomer(t *testing.T, ctx context.Context, harness *harness) string {
	t.Helper()
	var id string
	err := harness.pool.QueryRow(ctx, `INSERT INTO users (locale, timezone)
		VALUES ('en', 'UTC') RETURNING id::text`).Scan(&id)
	if err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	return id
}

// seedAttachmentRow writes a ticket, a message, and one attachment referencing
// the given digest, expired or not.
//
// Each row gets its own customer, because `support_tickets` carries a unique
// index allowing one open ticket per person. That is also the more faithful
// shape for the shared-digest case: two different customers uploading identical
// bytes is exactly how one file comes to back two rows.
func seedAttachmentRow(
	t *testing.T, ctx context.Context, harness *harness, digest string, expired bool,
) string {
	t.Helper()
	customerID := seedAttachmentCustomer(t, ctx, harness)

	// The 'general' queue is seeded by the support-desk migration, so the ticket
	// lands where an operator would actually see it.
	var ticketID string
	err := harness.pool.QueryRow(ctx, `INSERT INTO support_tickets (user_id, queue_id, subject, status)
		VALUES ($1::uuid, (SELECT id FROM support_queues WHERE code = 'general'), 'retention', 'open')
		RETURNING id::text`, customerID).Scan(&ticketID)
	if err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	var messageID int64
	err = harness.pool.QueryRow(ctx, `INSERT INTO support_messages (ticket_id, sender, body)
		VALUES ($1::uuid, 'customer', '[attachment]') RETURNING id`, ticketID).Scan(&messageID)
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}

	retain := "now() + interval '30 days'"
	if expired {
		retain = "now() - interval '1 day'"
	}
	var attachmentID string
	err = harness.pool.QueryRow(ctx, `INSERT INTO support_attachments
		(message_id, kind, origin, storage_key, file_name, mime_type, size_bytes, retain_until)
		VALUES ($1, 'document', 'local', $2, 'note.txt', 'text/plain', 12, `+retain+`)
		RETURNING id::text`, messageID, digest).Scan(&attachmentID)
	if err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	return attachmentID
}

func assertAttachmentRow(
	t *testing.T, ctx context.Context, harness *harness, attachmentID string, want bool,
) {
	t.Helper()
	var present bool
	err := harness.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM support_attachments WHERE id = $1::uuid)`, attachmentID,
	).Scan(&present)
	if err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	if present != want {
		t.Fatalf("attachment %s present = %v, want %v", attachmentID, present, want)
	}
}
