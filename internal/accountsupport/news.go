package accountsupport

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// The locales a post can be written in, matching
// `news_post_localizations.locale`.
const (
	localeRU = "ru"
	localeEN = "en"
)

// NewsItem is one published post in the customer's inbox.
type NewsItem struct {
	ID       string
	Slug     string
	Category string
	// Class separates a service announcement the customer will always receive
	// from a marketing post that needed consent. It is carried through to the
	// panel so the inbox can say which is which, not so this package can decide
	// whether either should have been sent.
	Class       string
	Title       string
	Body        string
	PublishedAt time.Time
	Read        bool
}

// NewsPage is one page of the inbox with the unread total the badge needs.
type NewsPage struct {
	Items      []NewsItem
	NextCursor string
	Unread     int
	// Locale is the locale the posts were actually resolved in, which is not
	// always the one that was asked for. The panel shows it rather than assuming
	// its own locale was honoured.
	Locale string
}

// newsVisible is the predicate for a post a customer may read.
//
// It is stricter than the bot's by one clause. Unpublishing keeps
// `published_at` set — deliberately, so republishing does not rewrite when
// customers first saw a post — which means a timestamp window alone would still
// show something an operator has taken down. A post is visible when its status
// says it is published and the window agrees.
const newsVisible = `n.status = 'published'
	AND n.published_at IS NOT NULL AND n.published_at <= now()
	AND (n.expires_at IS NULL OR n.expires_at > now())`

// News lists the announcements the customer can read, newest first.
func (service *Service) News(
	ctx context.Context, customerID, requested, cursor string, limit int,
) (NewsPage, error) {
	locale, err := service.resolveLocale(ctx, customerID, requested)
	if err != nil {
		return NewsPage{}, err
	}
	size := pageSize(limit, 20, 50)
	position := DecodeCursor(cursor)
	var cursorAt pgtype.Timestamptz
	var cursorID pgtype.UUID
	if position.set() {
		cursorAt = pgtype.Timestamptz{Time: position.At, Valid: true}
		if scanErr := cursorID.Scan(position.ID); scanErr != nil {
			cursorAt, cursorID = pgtype.Timestamptz{}, pgtype.UUID{}
		}
	}
	// A post with no translation in the resolved locale is absent rather than
	// shown in another language. That is the bot's rule too, and an inbox that
	// silently switches language mid-list is harder to read than a shorter one.
	rows, err := service.pool.Query(ctx, `SELECT n.id::text, n.slug, n.category, n.class,
		l.title, l.body, n.published_at,
		EXISTS (SELECT 1 FROM news_reads r WHERE r.post_id = n.id AND r.user_id = $1::uuid)
		FROM news_posts n
		JOIN news_post_localizations l ON l.post_id = n.id AND l.locale = $2
		WHERE `+newsVisible+`
		  AND ($3::timestamptz IS NULL
		       OR (n.published_at, n.id) < ($3::timestamptz, $4::uuid))
		ORDER BY n.published_at DESC, n.id DESC
		LIMIT $5`, customerID, locale, cursorAt, cursorID, size+1)
	if err != nil {
		return NewsPage{}, err
	}
	defer rows.Close()
	page := NewsPage{Items: make([]NewsItem, 0, size), Locale: locale}
	for rows.Next() {
		var item NewsItem
		if err = rows.Scan(&item.ID, &item.Slug, &item.Category, &item.Class,
			&item.Title, &item.Body, &item.PublishedAt, &item.Read); err != nil {
			return NewsPage{}, err
		}
		item.PublishedAt = item.PublishedAt.UTC()
		page.Items = append(page.Items, item)
	}
	if err = rows.Err(); err != nil {
		return NewsPage{}, err
	}
	if len(page.Items) > size {
		last := page.Items[size-1]
		page.Items = page.Items[:size]
		page.NextCursor = EncodeCursor(last.PublishedAt, last.ID)
	}
	if err = service.pool.QueryRow(ctx, `SELECT count(*)::integer
		FROM news_posts n
		JOIN news_post_localizations l ON l.post_id = n.id AND l.locale = $2
		WHERE `+newsVisible+`
		  AND NOT EXISTS (SELECT 1 FROM news_reads r WHERE r.post_id = n.id AND r.user_id = $1::uuid)`,
		customerID, locale).Scan(&page.Unread); err != nil {
		return NewsPage{}, err
	}
	return page, nil
}

// MarkNewsRead records that the customer opened a post.
//
// The row it writes is `news_reads`, which the bot reads and writes too, so a
// post opened in the browser stops being unread in Telegram and the other way
// round. Reading twice is not an error: the second call finds the row already
// there and reports the same outcome.
func (service *Service) MarkNewsRead(ctx context.Context, customerID, postID string) error {
	if !looksLikeUUID(strings.TrimSpace(postID)) {
		return ErrNotFound
	}
	var visible bool
	err := service.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM news_posts n WHERE n.id = $1::uuid AND `+newsVisible+`)`, postID).Scan(&visible)
	if err != nil {
		return err
	}
	if !visible {
		return ErrNotFound
	}
	_, err = service.pool.Exec(ctx, `INSERT INTO news_reads (user_id, post_id)
		VALUES ($1::uuid, $2::uuid) ON CONFLICT (user_id, post_id) DO NOTHING`, customerID, postID)
	return err
}

// resolveLocale decides which translation the customer sees.
//
// The request wins when it names a locale the content is actually written in,
// because the panel's own language switch is the most recent thing the customer
// touched. Otherwise the stored preference decides, and the account's locale is
// the fallback — the same order the bot resolves in, so one customer does not
// read the same announcement in two languages.
func (service *Service) resolveLocale(ctx context.Context, customerID, requested string) (string, error) {
	if normalised := normaliseLocale(requested); normalised != "" {
		return normalised, nil
	}
	var preference, account string
	err := service.pool.QueryRow(ctx, `SELECT COALESCE(p.locale, 'auto'), u.locale
		FROM users u LEFT JOIN bot_preferences p ON p.user_id = u.id
		WHERE u.id = $1::uuid`, customerID).Scan(&preference, &account)
	if errors.Is(err, pgx.ErrNoRows) {
		return localeEN, nil
	}
	if err != nil {
		return "", err
	}
	if normalised := normaliseLocale(preference); normalised != "" {
		return normalised, nil
	}
	if normalised := normaliseLocale(account); normalised != "" {
		return normalised, nil
	}
	return localeEN, nil
}

// normaliseLocale maps a supplied value onto a locale posts exist in, returning
// an empty string for anything else — including 'auto', which is a request to
// let the next fallback decide rather than a language.
func normaliseLocale(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case localeRU:
		return localeRU
	case localeEN:
		return localeEN
	default:
		return ""
	}
}
