// Package accountsupport is the customer web panel's support, news, and
// communication-preference surface.
//
// Its tickets are the operator support desk's tickets and the bot's tickets:
// one conversation per record, read by whichever surface the customer opened.
// That is why unread state lives on the message rather than on the surface — a
// reply read in Telegram must not still be bold on the web, and a customer who
// answers in a browser must appear in the operator queue exactly as one who
// answered in a chat.
//
// Two things this package will not do. It never returns an internal note: those
// are written by operators for operators and live in their own table, and the
// safest way to keep one out of a customer response is for the customer query
// never to touch that table. And it never decides whether a message may be sent
// to somebody — consent, suppression, quiet hours, and frequency caps are the
// communication pipeline's rules; this package only records what the customer
// chose.
package accountsupport

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/accountpg"
)

// The sentinels the transport maps onto responses.
//
// ErrNotFound covers every "you cannot see this" case on purpose. A ticket that
// belongs to somebody else and a ticket that never existed produce the same
// answer, so an identifier cannot be probed for existence by watching which
// error comes back.
var (
	// ErrNotFound reports a conversation, attachment, or post the calling
	// customer cannot see, whether because it is missing or because it is not
	// theirs.
	ErrNotFound = errors.New("not found")
	// ErrTicketClosed reports a write to a conversation that no longer accepts
	// one. Reopening is a separate, explicit action, so a reply typed into a
	// stale tab cannot silently revive a conversation both sides finished.
	ErrTicketClosed = errors.New("this conversation is closed")
	// ErrTooManyOpenTickets reports that the customer already holds as many open
	// conversations as Limits.MaxOpenTickets allows. The bound exists so a loop
	// in a client cannot fill the operator queue faster than people can empty it.
	ErrTooManyOpenTickets = errors.New("too many open conversations")
	// ErrAttachmentTooLarge and ErrAttachmentMediaType are separate because the
	// remedies are separate: one file can be made smaller, the other cannot be
	// made into an accepted type.
	ErrAttachmentTooLarge  = errors.New("the file is larger than the limit")
	ErrAttachmentMediaType = errors.New("that kind of file is not accepted")
	// ErrAttachmentRemote reports an attachment that arrived through Telegram.
	// Omniflow stores the reference rather than the bytes for those, so there is
	// nothing here to serve and saying so is more useful than a bare 404.
	ErrAttachmentRemote = errors.New("this file can only be opened in Telegram")
	// ErrAttachmentStorage reports that the configured attachment directory could
	// not be read or written. It is an installation fault rather than a customer
	// one, so it answers as a temporary unavailability.
	ErrAttachmentStorage = errors.New("attachment storage is unavailable")
	// ErrQueueMissing reports an installation with no default support queue. Every
	// ticket needs somewhere to go, and creating one that lands nowhere would put
	// a customer's question outside every operator's view.
	ErrQueueMissing = errors.New("no default support queue is configured")
)

// invalidInput carries a customer-facing reason while still matching
// accountpg.ErrInvalidInput, so the shared error writer answers 422 and shows
// the reason without this package having to restate the mapping.
type invalidInput struct{ reason string }

func (err invalidInput) Error() string { return err.reason }

// Is makes errors.Is(err, accountpg.ErrInvalidInput) true for every value of
// this type, which is what lets one sentinel carry many different reasons.
func (invalidInput) Is(target error) bool { return target == accountpg.ErrInvalidInput }

func invalid(reason string) error { return invalidInput{reason: reason} }

// Limits are the attachment restrictions the panel enforces alongside the API.
// They mirror the bot's, because one ticket can carry attachments from both.
type Limits struct {
	// MaxAttachmentBytes refuses an upload larger than the support desk stores.
	MaxAttachmentBytes int64
	// AllowedMediaTypes is the exact set accepted. An allowlist rather than a
	// blocklist: an unknown type is refused, not guessed at.
	AllowedMediaTypes []string
	// MaxOpenTickets bounds how many conversations one customer may have open,
	// so a loop cannot fill the operator queue.
	MaxOpenTickets int
}

// DefaultLimits are the values used when an installation configures none.
func DefaultLimits() Limits {
	return Limits{
		MaxAttachmentBytes: 5 << 20,
		AllowedMediaTypes: []string{
			"image/png", "image/jpeg", "image/webp", "application/pdf", "text/plain",
		},
		MaxOpenTickets: 5,
	}
}

// schemaMaxAttachmentBytes is the ceiling `support_attachments.size_bytes`
// enforces. An installation may configure a smaller limit but never a larger
// one, because a row above this size cannot be written at all and the customer
// would learn that only after their upload finished.
const schemaMaxAttachmentBytes = 10 * 1024 * 1024

// RequestContext names the surface and the request that recorded a customer's
// choice. It is what a consent record is written with, so a later question about
// where an opt-in came from has an answer that does not depend on memory.
type RequestContext struct {
	// Surface defaults to customerWebSurface when empty.
	Surface   string
	RequestID string
}

// customerWebSurface is the value written into `consent_records.source` for a
// choice made in the browser. The bot writes 'telegram_bot' into the same
// column, so the two surfaces stay distinguishable in the consent history.
const customerWebSurface = "customer_web"

func (context RequestContext) surface() string {
	if strings.TrimSpace(context.Surface) == "" {
		return customerWebSurface
	}
	return context.Surface
}

// Service is the customer support and communication adapter.
type Service struct {
	pool        *pgxpool.Pool
	limits      Limits
	attachments AttachmentStore
	logger      *slog.Logger
	clock       func() time.Time
}

// Options configures a Service.
type Options struct {
	Limits Limits
	// Attachments overrides where uploaded bytes are kept. Left nil, a directory
	// store rooted at AttachmentDirectory is used.
	Attachments AttachmentStore
	// AttachmentDirectory defaults to defaultAttachmentDirectory, which follows
	// the same convention as the backup directory: a fixed path under
	// /var/lib/omniflow that a deployment mounts a volume at.
	AttachmentDirectory string
	Logger              *slog.Logger
	Clock               func() time.Time
}

// New builds the adapter.
func New(pool *pgxpool.Pool, options Options) (*Service, error) {
	if pool == nil {
		return nil, errors.New("a database pool is required")
	}
	limits := options.Limits
	if limits.MaxAttachmentBytes <= 0 {
		limits = DefaultLimits()
	}
	if limits.MaxAttachmentBytes > schemaMaxAttachmentBytes {
		limits.MaxAttachmentBytes = schemaMaxAttachmentBytes
	}
	if limits.MaxOpenTickets <= 0 {
		limits.MaxOpenTickets = DefaultLimits().MaxOpenTickets
	}
	store := options.Attachments
	if store == nil {
		directory := options.AttachmentDirectory
		if strings.TrimSpace(directory) == "" {
			directory = defaultAttachmentDirectory
		}
		store = DirectoryStore{Root: directory}
	}
	service := &Service{
		pool: pool, limits: limits, attachments: store,
		logger: options.Logger, clock: options.Clock,
	}
	if service.logger == nil {
		service.logger = slog.Default()
	}
	if service.clock == nil {
		service.clock = time.Now
	}
	return service, nil
}

func (service *Service) now() time.Time { return service.clock().UTC() }

// Limits exposes the attachment rules so the panel can state them before an
// upload rather than only after one is refused.
func (service *Service) Limits() Limits { return service.limits }

// ---------------------------------------------------------------------------
// Cursor pagination
// ---------------------------------------------------------------------------

// Cursor is a keyset position: the instant a page ended at and the identifier
// that broke the tie. Offsets would drift under a list that reorders whenever
// somebody replies, which is exactly what a support inbox does.
type Cursor struct {
	At time.Time
	ID string
}

// EncodeCursor renders a position. An empty identifier yields an empty cursor,
// which is how "there is no next page" is expressed.
func EncodeCursor(at time.Time, id string) string {
	if id == "" {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano) + "|" + id
}

// DecodeCursor parses a position. A malformed value yields the zero cursor
// rather than an error: the right answer to an unreadable cursor is the first
// page, not a failed request.
func DecodeCursor(value string) Cursor {
	parts := strings.SplitN(strings.TrimSpace(value), "|", 2)
	if len(parts) != 2 {
		return Cursor{}
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return Cursor{}
	}
	if !looksLikeUUID(parts[1]) {
		return Cursor{}
	}
	return Cursor{At: at.UTC(), ID: parts[1]}
}

// set reports whether the cursor points somewhere, which the queries read as
// "continue from here" rather than "start at the beginning".
func (cursor Cursor) set() bool { return cursor.ID != "" }

// looksLikeUUID is a shape check, not a validation. It exists so a cursor
// fragment is never interpolated into a query as an identifier the database
// would have to reject; the database still does the real parsing.
func looksLikeUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		switch index {
		case 8, 13, 18, 23:
			if character != '-' {
				return false
			}
		default:
			isDigit := character >= '0' && character <= '9'
			isHex := character >= 'a' && character <= 'f' || character >= 'A' && character <= 'F'
			if !isDigit && !isHex {
				return false
			}
		}
	}
	return true
}

// pageSize clamps a requested page size onto something a single response can
// carry, so a caller asking for everything gets a page rather than a timeout.
func pageSize(requested, fallback, maximum int) int {
	if requested <= 0 {
		return fallback
	}
	if requested > maximum {
		return maximum
	}
	return requested
}

// ownsTicket reports whether the conversation belongs to the customer, and is
// the single ownership check every ticket action starts from.
//
// It reads support_tickets alone. No customer-facing query in this package
// touches support_notes, and that is deliberate: a note is written by an
// operator for other operators, and the way to guarantee one never reaches a
// customer is for the customer's queries not to know the table exists.
func (service *Service) ownsTicket(ctx context.Context, customerID, ticketID string) error {
	if !looksLikeUUID(strings.TrimSpace(ticketID)) {
		return ErrNotFound
	}
	var exists bool
	err := service.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM support_tickets WHERE id = $2::uuid AND user_id = $1::uuid)`,
		customerID, ticketID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// truncateRunes bounds a value by characters rather than bytes, because the
// column checks are character counts and a Cyrillic subject would otherwise be
// refused at half the length an English one is allowed.
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// firstLine is the fallback subject for a conversation that was opened without
// one, matching what the bot writes so the two surfaces name a ticket the same
// way.
func firstLine(body string) string {
	line := body
	if index := strings.IndexAny(line, "\r\n"); index >= 0 {
		line = line[:index]
	}
	return strings.TrimSpace(line)
}
