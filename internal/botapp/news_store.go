package botapp

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// NewsItem is one published post in the customer's inbox.
type NewsItem struct {
	ID          string
	Slug        string
	Category    string
	Class       string
	Title       string
	Body        string
	PublishedAt time.Time
	Read        bool
}

// ErrNewsNotFound is returned when a post is unpublished, expired, or missing a
// translation for the requested locale.
var ErrNewsNotFound = errors.New("news post not found")

const newsColumns = `n.id::text, n.slug, n.category, n.class, l.title, l.body, n.published_at,
	EXISTS (SELECT 1 FROM news_reads r WHERE r.post_id = n.id AND r.user_id = $1::uuid)`

// newsVisible is what a customer may read.
//
// `status` is checked as well as `published_at` because unpublishing a post does
// not clear the date it went out on: an operator who takes a post down leaves
// `published_at` set and moves `status` to 'unpublished' or 'archived'. Reading
// only the date would keep showing a withdrawn post in Telegram while the web
// panel, which does check the status, had already stopped — and the two surfaces
// must not disagree about what an operator has taken down.
const newsVisible = `n.status = 'published'
	AND n.published_at IS NOT NULL AND n.published_at <= now()
	AND (n.expires_at IS NULL OR n.expires_at > now())`

// News lists the posts a customer can read, newest first.
//
// The identifier breaks the tie on the timestamp. Two posts published in the
// same instant — a batch an operator released together, or a scheduled run —
// otherwise come back in whatever order the scan produced, and the web panel
// orders the same rows deterministically. One customer reading their
// announcements in a chat and in a browser must not see them in two different
// orders.
func (store *PostgresStore) News(ctx context.Context, customerID string, locale Locale, limit int) ([]NewsItem, error) {
	rows, err := store.pool.Query(ctx, `SELECT `+newsColumns+`
		FROM news_posts n
		JOIN news_post_localizations l ON l.post_id = n.id AND l.locale = $2
		WHERE `+newsVisible+`
		ORDER BY n.published_at DESC, n.id DESC LIMIT $3`, customerID, string(locale), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]NewsItem, 0, limit)
	for rows.Next() {
		item, scanErr := scanNews(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// NewsItem reads one post and marks nothing; reading is recorded explicitly.
func (store *PostgresStore) NewsItem(ctx context.Context, customerID, postID string, locale Locale) (NewsItem, error) {
	item, err := scanNews(store.pool.QueryRow(ctx, `SELECT `+newsColumns+`
		FROM news_posts n
		JOIN news_post_localizations l ON l.post_id = n.id AND l.locale = $2
		WHERE n.id = $3::uuid AND `+newsVisible, customerID, string(locale), postID))
	if errors.Is(err, pgx.ErrNoRows) {
		return NewsItem{}, ErrNewsNotFound
	}
	return item, err
}

// UnreadNewsCount counts published posts the customer has not opened.
func (store *PostgresStore) UnreadNewsCount(ctx context.Context, customerID string, locale Locale) (int, error) {
	var unread int
	err := store.pool.QueryRow(ctx, `SELECT count(*)::integer
		FROM news_posts n
		JOIN news_post_localizations l ON l.post_id = n.id AND l.locale = $2
		WHERE `+newsVisible+`
		  AND NOT EXISTS (SELECT 1 FROM news_reads r WHERE r.post_id = n.id AND r.user_id = $1::uuid)`,
		customerID, string(locale)).Scan(&unread)
	return unread, err
}

// MarkNewsRead records that the customer opened a post.
func (store *PostgresStore) MarkNewsRead(ctx context.Context, customerID, postID string) error {
	_, err := store.pool.Exec(ctx, `INSERT INTO news_reads (user_id, post_id)
		VALUES ($1::uuid, $2::uuid) ON CONFLICT (user_id, post_id) DO NOTHING`, customerID, postID)
	return err
}

// PendingNewsAnnouncement is a published post that still has to be announced to
// a customer's Telegram chat.
type PendingNewsAnnouncement struct {
	PostID     string
	CustomerID string
	TelegramID int64
	Category   string
	Class      string
	Title      string
	Locale     string
}

// PendingNewsAnnouncements finds customers who have not yet been told about a
// recently published post. Consent, quiet hours, and frequency caps are applied
// by the delivery pipeline, not by this query.
func (store *PostgresStore) PendingNewsAnnouncements(ctx context.Context, since time.Duration, limit int) ([]PendingNewsAnnouncement, error) {
	rows, err := store.pool.Query(ctx, telegramRecipients+`
		SELECT DISTINCT ON (n.id, u.id) n.id::text, u.id::text, recipient.telegram_id, n.category, n.class, l.title,
		CASE WHEN COALESCE(p.locale, 'auto') = 'auto' THEN u.locale ELSE p.locale END AS locale
		FROM news_posts n
		JOIN users u ON u.status = 'active'
		JOIN recipient ON recipient.user_id = u.id
		LEFT JOIN bot_preferences p ON p.user_id = u.id
		JOIN news_post_localizations l ON l.post_id = n.id
			AND l.locale = CASE WHEN COALESCE(p.locale, 'auto') = 'auto' THEN u.locale ELSE p.locale END
		WHERE n.status = 'published'
		  AND n.published_at IS NOT NULL AND n.published_at <= now()
		  AND n.published_at > now() - $1::interval
		  AND (n.expires_at IS NULL OR n.expires_at > now())
		  AND NOT EXISTS (
			SELECT 1 FROM notification_deliveries d
			WHERE d.user_id = u.id AND d.kind = n.category AND d.dedupe_key = n.id::text)
		ORDER BY n.id, u.id, n.published_at DESC LIMIT $2`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	announcements := make([]PendingNewsAnnouncement, 0, limit)
	for rows.Next() {
		var announcement PendingNewsAnnouncement
		if err := rows.Scan(&announcement.PostID, &announcement.CustomerID, &announcement.TelegramID,
			&announcement.Category, &announcement.Class, &announcement.Title, &announcement.Locale); err != nil {
			return nil, err
		}
		announcements = append(announcements, announcement)
	}
	return announcements, rows.Err()
}

func scanNews(row pgx.Row) (NewsItem, error) {
	var item NewsItem
	err := row.Scan(&item.ID, &item.Slug, &item.Category, &item.Class, &item.Title, &item.Body, &item.PublishedAt, &item.Read)
	return item, err
}
