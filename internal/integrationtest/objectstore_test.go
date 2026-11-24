//go:build integration

package integrationtest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	telegram "github.com/go-telegram/bot"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/objectstore"
)

const seaweedConfig = `{"identities":[{"name":"omniflow","credentials":[{"accessKey":"test-access","secretKey":"test-secret-key"}],"actions":["Admin","Read","Write","List","Tagging"]}]}`

// seaweedStore starts SeaweedFS, the S3 server production runs beside the
// stack, and returns the configuration that reaches it.
func seaweedStore(t *testing.T) config.ObjectStoreConfig {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.Run(ctx, "chrislusf/seaweedfs:4.55",
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader: strings.NewReader(seaweedConfig), ContainerFilePath: "/etc/seaweedfs/s3.json", FileMode: 0o644,
		}),
		testcontainers.WithCmd("server", "-dir=/data", "-s3", "-s3.port=8333", "-s3.config=/etc/seaweedfs/s3.json"),
		testcontainers.WithExposedPorts("8333/tcp"),
		testcontainers.WithWaitStrategyAndDeadline(2*time.Minute,
			wait.ForHTTP("/").WithPort("8333/tcp").WithStatusCodeMatcher(func(int) bool { return true })),
	)
	if err != nil {
		t.Fatalf("start seaweedfs: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	endpoint, err := container.PortEndpoint(ctx, "8333/tcp", "")
	if err != nil {
		t.Fatalf("seaweedfs endpoint: %v", err)
	}
	return config.ObjectStoreConfig{
		Endpoint: endpoint, AccessKey: "test-access", SecretKey: "test-secret-key",
		Bucket: "omniflow-test", Region: "us-east-1", Secure: false,
	}
}

// putEventually retries the first write while the store finishes starting.
func putEventually(t *testing.T, store *objectstore.Store, key string, content []byte) {
	t.Helper()
	var err error
	for attempt := 0; attempt < 30; attempt++ {
		if err = store.Put(context.Background(), key, content); err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("put: %v", err)
}

// A file a customer sends the bot is copied into the object store and opens
// from the operator desk.
func TestTelegramAttachmentsAreCopiedToTheObjectStore(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	cfg := seaweedStore(t)
	store, err := objectstore.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// The store itself: put, open, a missing key, remove.
	key := strings.Repeat("ab", 32)
	putEventually(t, store, key, []byte("hello"))
	if content, openErr := store.Open(ctx, key); openErr != nil || string(content) != "hello" {
		t.Fatalf("open = %q, %v", content, openErr)
	}
	if err := store.Remove(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(ctx, key); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a removed key: %v", err)
	}
	if err := store.Remove(ctx, key); err != nil {
		t.Fatalf("removing a missing key: %v", err)
	}

	// A fake Bot API that hands over one photo.
	photo := bytes.Repeat([]byte{0xff, 0xd8, 0xff}, 1000)
	telegramAPI := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/getFile"):
			writer.Header().Set("Content-Type", "application/json")
			body, _ := io.ReadAll(request.Body)
			if bytes.Contains(body, []byte("gone")) {
				_, _ = writer.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: invalid file_id"}`))
				return
			}
			_, _ = writer.Write([]byte(`{"ok":true,"result":{"file_id":"photo-1","file_unique_id":"u1","file_path":"photos/file_1.jpg"}}`))
		case strings.HasSuffix(request.URL.Path, "/photos/file_1.jpg"):
			_, _ = writer.Write(photo)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(telegramAPI.Close)
	client, err := telegram.New("123:test", telegram.WithServerURL(telegramAPI.URL), telegram.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}

	customer := harness.customer(ctx, t)
	var ticketID, attachmentID, missingID string
	var messageID int64
	if err := harness.pool.QueryRow(ctx, `INSERT INTO support_tickets (user_id, queue_id, subject)
		SELECT $1::uuid, id, 'Screenshot' FROM support_queues WHERE is_default AND archived_at IS NULL
		RETURNING id::text`, customer).Scan(&ticketID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	if err := harness.pool.QueryRow(ctx, `INSERT INTO support_messages (ticket_id, sender, body)
		VALUES ($1::uuid, 'customer', '[attachment]') RETURNING id`, ticketID).Scan(&messageID); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := harness.pool.QueryRow(ctx, `INSERT INTO support_attachments (message_id, kind, origin, telegram_file_id, size_bytes)
		VALUES ($1, 'photo', 'telegram', 'photo-1', $2) RETURNING id::text`, messageID, len(photo)).Scan(&attachmentID); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
	// A file Telegram does not hand over is tried a bounded number of times.
	if err := harness.pool.QueryRow(ctx, `INSERT INTO support_attachments (message_id, kind, origin, telegram_file_id, size_bytes)
		VALUES ($1, 'document', 'telegram', 'gone', 10) RETURNING id::text`, messageID).Scan(&missingID); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}

	panel := newOperations(t, harness)
	if meta, _, err := panel.TicketAttachment(ctx, ticketID, attachmentID); err != nil || meta.Downloadable {
		t.Fatalf("before the copy: %+v, %v", meta, err)
	}

	copier := botapp.NewAttachmentCopier(harness.pool, client, store, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	copier.CopyPending(ctx)

	meta, storageKey, err := panel.TicketAttachment(ctx, ticketID, attachmentID)
	if err != nil || !meta.Downloadable || storageKey == "" {
		t.Fatalf("after the copy: %+v, key %q, %v", meta, storageKey, err)
	}
	if content, err := store.Open(ctx, storageKey); err != nil || !bytes.Equal(content, photo) {
		t.Fatalf("the copy holds %d bytes, %v", len(content), err)
	}
	var attempts int
	if err := harness.pool.QueryRow(ctx, `SELECT fetch_attempts FROM support_attachments WHERE id = $1::uuid`, missingID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("the failed file's attempts = %d, %v", attempts, err)
	}
	for range 6 {
		copier.CopyPending(ctx)
	}
	if err := harness.pool.QueryRow(ctx, `SELECT fetch_attempts FROM support_attachments WHERE id = $1::uuid`, missingID).Scan(&attempts); err != nil || attempts != 5 {
		t.Fatalf("a file Telegram refuses is tried %d times, %v", attempts, err)
	}
}
