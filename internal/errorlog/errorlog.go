// Package errorlog keeps a record of the errors a process logs, so an operator
// sees them in the panel's error log and hears about a new one in the
// operator group, without reading container logs.
//
// It is a slog handler wrapped around the process's own: every record still
// reaches the original handler unchanged, and records at error level are
// additionally grouped by a fingerprint and written to `app_errors` from a
// background goroutine. Nothing here can block or fail the code that logged:
// the queue is bounded and a full queue drops the copy, never the log line.
//
// The operator group is told the process, the fingerprint, and the count —
// never the message, which is shown only in the panel to operators holding
// system.read, because a log message can quote a value an operator group chat
// should not keep.
package errorlog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Handler copies error records into the error log.
type Handler struct {
	inner   slog.Handler
	state   *state
	attrs   []slog.Attr
	process string
}

type state struct {
	process string
	queue   chan entry
	pool    atomic.Pointer[pgxpool.Pool]
}

type entry struct {
	message string
	source  string
}

// queueSize bounds how many unwritten errors a process holds. An error storm
// past it drops copies rather than memory.
const queueSize = 256

// New wraps a handler for one process: "api", "bot", or "worker".
func New(inner slog.Handler, process string) *Handler {
	return &Handler{inner: inner, process: process, state: &state{process: process, queue: make(chan entry, queueSize)}}
}

// Attach starts writing to the database. Records logged before it are only
// passed through, because the database is what most start-up errors are about.
func (handler *Handler) Attach(ctx context.Context, pool *pgxpool.Pool) {
	if handler.state.pool.Swap(pool) == nil {
		go handler.state.drain(ctx)
	}
}

// Enabled defers to the wrapped handler.
func (handler *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return handler.inner.Enabled(ctx, level) || level >= slog.LevelError
}

// Handle passes the record on and, at error level, queues a copy.
func (handler *Handler) Handle(ctx context.Context, record slog.Record) error {
	if record.Level >= slog.LevelError && handler.state.pool.Load() != nil {
		message := record.Message
		attributes := append([]slog.Attr{}, handler.attrs...)
		record.Attrs(func(attribute slog.Attr) bool {
			attributes = append(attributes, attribute)
			return true
		})
		for _, attribute := range attributes {
			if attribute.Key == "error" {
				message += ": " + attribute.Value.String()
			}
		}
		source := ""
		for _, attribute := range attributes {
			if attribute.Key == "task" || attribute.Key == "kind" || attribute.Key == "component" {
				source = attribute.Value.String()
			}
		}
		select {
		case handler.state.queue <- entry{message: message, source: source}:
		default:
		}
	}
	if !handler.inner.Enabled(ctx, record.Level) {
		return nil
	}
	return handler.inner.Handle(ctx, record)
}

// WithAttrs keeps the wrapper around the derived handler.
func (handler *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{inner: handler.inner.WithAttrs(attrs), state: handler.state, process: handler.process,
		attrs: append(append([]slog.Attr{}, handler.attrs...), attrs...)}
}

// WithGroup keeps the wrapper around the derived handler.
func (handler *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: handler.inner.WithGroup(name), state: handler.state, process: handler.process, attrs: handler.attrs}
}

func (state *state) drain(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-state.queue:
			state.record(ctx, item)
		}
	}
}

func (state *state) record(ctx context.Context, item entry) {
	pool := state.pool.Load()
	if pool == nil {
		return
	}
	message := truncate(item.message, 1000)
	fingerprint := Fingerprint(state.process, message)
	queries := dbgen.New(pool)
	row, err := queries.RecordAppError(ctx, dbgen.RecordAppErrorParams{
		Fingerprint: fingerprint, Process: state.process, Message: message, Source: truncate(item.source, 120),
	})
	if err != nil {
		return
	}
	if !row.Inserted && !milestone(row.Occurrences) {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"process": state.process, "fingerprint": fingerprint[:12], "occurrences": row.Occurrences,
	})
	_, _ = queries.EnqueueOperatorNotice(ctx, dbgen.EnqueueOperatorNoticeParams{
		Kind: "error", DedupeKey: fmt.Sprintf("error:%s:%d", fingerprint, row.Occurrences), Payload: payload,
	})
}

// milestone is when a recurring error is announced again: at 10, 100, 1000…
func milestone(occurrences int64) bool {
	for occurrences >= 10 && occurrences%10 == 0 {
		occurrences /= 10
	}
	return occurrences == 1
}

var variable = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F-]{27}|\b\d+\b`)

// Fingerprint groups errors that differ only in identifiers and numbers.
func Fingerprint(process, message string) string {
	digest := sha256.Sum256([]byte(process + "\x00" + variable.ReplaceAllString(message, "#")))
	return hex.EncodeToString(digest[:])
}

func truncate(value string, limit int) string {
	value = strings.ToValidUTF8(value, "")
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}
