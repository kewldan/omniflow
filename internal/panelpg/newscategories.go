package panelpg

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// NewsCategory is one category an operator files news under. Kind is the
// notification kind its posts are announced as — one of the four kinds a
// customer can mute — so a new category never escapes a customer's choice.
type NewsCategory struct {
	Code      string `json:"code"`
	NameRU    string `json:"nameRu"`
	NameEN    string `json:"nameEn"`
	Color     string `json:"color"`
	Kind      string `json:"kind"`
	SortOrder int32  `json:"sortOrder"`
	// Posts counts the posts filed under it; a category with posts cannot
	// be deleted.
	Posts int64 `json:"posts"`
}

// ErrNewsCategoryInUse refuses to delete a category posts are filed under.
var ErrNewsCategoryInUse = fmt.Errorf("%w: news posts are filed under this category", ErrConflict)

var (
	newsCategoryCode  = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)
	newsCategoryColor = regexp.MustCompile(`^#[0-9a-f]{6}$`)
	newsKinds         = map[string]bool{"news": true, "announcement": true, "incident": true, "maintenance": true}
	newsTag           = regexp.MustCompile(`^[a-zа-яё0-9_]{1,32}$`)
)

// NewsCategories lists the categories in their display order.
func (service *Service) NewsCategories(ctx context.Context) ([]NewsCategory, error) {
	rows, err := service.pool.Query(ctx, `SELECT c.code, c.name_ru, c.name_en, c.color, c.kind, c.sort_order,
			(SELECT count(*) FROM news_posts p WHERE p.category_code = c.code)
		FROM news_categories c ORDER BY c.sort_order, c.code`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (NewsCategory, error) {
		var category NewsCategory
		err := row.Scan(&category.Code, &category.NameRU, &category.NameEN, &category.Color, &category.Kind,
			&category.SortOrder, &category.Posts)
		return category, err
	})
}

// SaveNewsCategory creates a category or, for an existing code, renames,
// recolours, reorders, or changes its notification kind. Posts already filed
// under it take the new kind in the same transaction, so a later
// announcement goes out under the kind the operator now chose.
func (service *Service) SaveNewsCategory(ctx context.Context, category NewsCategory, actor Actor) (NewsCategory, error) {
	category.Code = strings.ToLower(strings.TrimSpace(category.Code))
	category.NameRU, category.NameEN = strings.TrimSpace(category.NameRU), strings.TrimSpace(category.NameEN)
	category.Color = strings.ToLower(strings.TrimSpace(category.Color))
	if category.Kind == "" {
		category.Kind = "news"
	}
	if !newsCategoryCode.MatchString(category.Code) || !newsCategoryColor.MatchString(category.Color) ||
		!newsKinds[category.Kind] || category.NameRU == "" || category.NameEN == "" ||
		utf8.RuneCountInString(category.NameRU) > 40 || utf8.RuneCountInString(category.NameEN) > 40 {
		return NewsCategory{}, ErrValidaton
	}
	err := service.inTxRaw(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		if _, err := tx.Exec(ctx, `INSERT INTO news_categories (code, name_ru, name_en, color, kind, sort_order)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (code) DO UPDATE SET name_ru = EXCLUDED.name_ru, name_en = EXCLUDED.name_en,
				color = EXCLUDED.color, kind = EXCLUDED.kind, sort_order = EXCLUDED.sort_order, updated_at = now()`,
			category.Code, category.NameRU, category.NameEN, category.Color, category.Kind, category.SortOrder); err != nil {
			return err
		}
		// Re-deriving the kind is the trigger's job; touching the column fires it.
		if _, err := tx.Exec(ctx, `UPDATE news_posts SET category_code = category_code
			WHERE category_code = $1 AND category <> $2`, category.Code, category.Kind); err != nil {
			return err
		}
		return appendAudit(ctx, queries, actor.audit("news.category_saved", "marketing", "news_category", category.Code,
			map[string]any{"kind": category.Kind, "color": category.Color}))
	})
	return category, err
}

// DeleteNewsCategory removes a category no post is filed under.
func (service *Service) DeleteNewsCategory(ctx context.Context, code string, actor Actor) error {
	return service.inTxRaw(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		var posts int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM news_posts WHERE category_code = $1`, code).Scan(&posts); err != nil {
			return err
		}
		if posts > 0 {
			return ErrNewsCategoryInUse
		}
		removed, err := tx.Exec(ctx, `DELETE FROM news_categories WHERE code = $1`, code)
		if err != nil {
			return err
		}
		if removed.RowsAffected() == 0 {
			return ErrNotFound
		}
		return appendAudit(ctx, queries, actor.audit("news.category_deleted", "marketing", "news_category", code, nil))
	})
}

// normalizeNewsTags lower-cases tags, drops a leading '#', removes duplicates,
// and refuses a tag outside letters, digits, and '_' or more than ten tags.
func normalizeNewsTags(tags []string) ([]string, error) {
	seen := map[string]bool{}
	normalized := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(tag), "#"))
		if tag == "" || seen[tag] {
			continue
		}
		if !newsTag.MatchString(tag) {
			return nil, ErrValidaton
		}
		seen[tag] = true
		normalized = append(normalized, tag)
	}
	if len(normalized) > 10 {
		return nil, ErrValidaton
	}
	return normalized, nil
}

// validNewsCover accepts an absolute https address with a host and nothing a
// renderer could mistake for markup. The cover is shown to customers in the
// bot and the browser, so anything else is refused rather than escaped.
func validNewsCover(raw string) bool {
	if len(raw) > 2000 || strings.ContainsAny(raw, " \t\r\n<>\"'`\\") {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}
