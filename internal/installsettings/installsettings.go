// Package installsettings reads the installation settings an operator saves in
// the panel, for the processes whose behaviour they change.
//
// A section is one JSON document in `installation_settings`. The panel writes
// it with a version guard and an audit event; this package is the read side.
// Every reader asks for a value together with the fallback it uses when the
// operator has not set one, which is the environment variable or compiled-in
// default the process would have used anyway. So an installation that never
// opens the settings screen behaves exactly as it did before, and a value an
// operator saves takes effect on the next read, within one cache period,
// without a restart.
//
// Secrets are not read here. A credential a process needs before it can do
// anything — the Remnawave token, the bot token, the backup key — stays in the
// environment, and the panel does not offer to store a second copy of it.
package installsettings

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// DefaultTTL is how long a section document is trusted before it is re-read.
// It is short enough that a saved value applies "almost at once" and long
// enough that a hot path reading a value per request costs one query a minute.
const DefaultTTL = time.Minute

// Document is one section's saved values.
type Document map[string]any

// Reader reads and caches section documents.
type Reader struct {
	pool   *pgxpool.Pool
	ttl    time.Duration
	logger *slog.Logger
	clock  func() time.Time

	mu      sync.Mutex
	entries map[string]entry
}

type entry struct {
	document Document
	readAt   time.Time
}

// New builds a reader. A nil pool yields a reader that always answers with the
// fallback, which is what a unit test or a process without a database wants.
func New(pool *pgxpool.Pool, logger *slog.Logger) *Reader {
	if logger == nil {
		logger = slog.Default()
	}
	return &Reader{
		pool: pool, ttl: DefaultTTL, logger: logger, clock: time.Now,
		entries: map[string]entry{},
	}
}

// Static builds a reader that serves fixed documents and never touches a
// database. Tests use it to drive a component through a saved setting.
func Static(documents map[string]Document) *Reader {
	reader := New(nil, nil)
	reader.ttl = math.MaxInt64
	for section, document := range documents {
		reader.entries[section] = entry{document: document, readAt: time.Now()}
	}
	return reader
}

// Section returns the saved document of one section. A read failure is logged
// and answered with the last good copy, or an empty document, because a
// settings outage must never stop a process that has its own defaults.
func (reader *Reader) Section(ctx context.Context, section string) Document {
	if reader == nil {
		return Document{}
	}
	reader.mu.Lock()
	cached, ok := reader.entries[section]
	reader.mu.Unlock()
	now := reader.clock()
	if ok && now.Sub(cached.readAt) < reader.ttl {
		return cached.document
	}
	if reader.pool == nil {
		return cached.document
	}
	document := Document{}
	row, err := dbgen.New(reader.pool).GetSettingSection(ctx, section)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		reader.logger.Warn("installation settings read failed", "section", section, "error", err)
		if ok {
			return cached.document
		}
		return document
	default:
		if decodeErr := json.Unmarshal(row.Document, &document); decodeErr != nil {
			reader.logger.Warn("installation settings document is not an object", "section", section)
			document = Document{}
		}
	}
	reader.mu.Lock()
	reader.entries[section] = entry{document: document, readAt: now}
	reader.mu.Unlock()
	return document
}

// Invalidate drops a cached section, so the next read sees a save at once.
// The api calls it after the panel saves a section in the same process.
func (reader *Reader) Invalidate(section string) {
	if reader == nil {
		return
	}
	reader.mu.Lock()
	delete(reader.entries, section)
	reader.mu.Unlock()
}

// Int reads a whole number. Absent, empty, or not a number answers fallback.
func (document Document) Int(name string, fallback int) int {
	switch value := document[name].(type) {
	case float64:
		if value == math.Trunc(value) && math.Abs(value) < math.MaxInt32 {
			return int(value)
		}
	case string:
		// Parsed at 32 bits, the same range the number branch accepts, so a
		// saved value can never wrap when a reader narrows it for a query.
		if parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32); err == nil {
			return int(parsed)
		}
	}
	return fallback
}

// Positive reads a whole number that must be above zero; anything else,
// including a saved zero, answers fallback. It is the reader for intervals and
// limits, where zero would mean "never" or "nothing" by accident.
func (document Document) Positive(name string, fallback int) int {
	if value := document.Int(name, fallback); value > 0 {
		return value
	}
	return fallback
}

// Bool reads a switch. Absent answers fallback, so an unsaved switch never
// overrides the environment.
func (document Document) Bool(name string, fallback bool) bool {
	switch value := document[name].(type) {
	case bool:
		return value
	case string:
		if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return fallback
}

// Has reports whether the operator saved a value for a field.
func (document Document) Has(name string) bool {
	value, ok := document[name]
	if !ok || value == nil {
		return false
	}
	if text, isText := value.(string); isText {
		return strings.TrimSpace(text) != ""
	}
	return true
}

// String reads text. Absent or blank answers fallback.
func (document Document) String(name, fallback string) string {
	if value, ok := document[name].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

// Strings reads a list of text values. A saved list replaces fallback even
// when it is empty, because "nothing" is a choice an operator can make; a
// comma-separated string is accepted too, which is what the generic settings
// renderer's text field saves.
func (document Document) Strings(name string, fallback []string) []string {
	switch value := document[name].(type) {
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	case string:
		out := []string{}
		for item := range strings.SplitSeq(value, ",") {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	}
	return fallback
}

// Duration reads a whole number of units, positive only.
func (document Document) Duration(name string, unit, fallback time.Duration) time.Duration {
	if value := document.Int(name, 0); value > 0 {
		return time.Duration(value) * unit
	}
	return fallback
}

// Location reads the branding timezone, falling back to UTC.
func (reader *Reader) Location(ctx context.Context) *time.Location {
	name := reader.Section(ctx, "branding").String("timezone", "UTC")
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

// ClockMinutes parses "HH:MM" into minutes past midnight.
func ClockMinutes(value string) (int, bool) {
	hours, minutes, found := strings.Cut(strings.TrimSpace(value), ":")
	if !found {
		return 0, false
	}
	hour, hourErr := strconv.Atoi(hours)
	minute, minuteErr := strconv.Atoi(minutes)
	if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}

// WithinWindow reports whether a local time falls inside [start, end), where
// the window may wrap past midnight ("23:00"–"07:00"). An unparseable or empty
// window is never active.
func WithinWindow(local time.Time, start, end string) bool {
	from, okFrom := ClockMinutes(start)
	to, okTo := ClockMinutes(end)
	if !okFrom || !okTo || from == to {
		return false
	}
	now := local.Hour()*60 + local.Minute()
	if from < to {
		return now >= from && now < to
	}
	return now >= from || now < to
}
