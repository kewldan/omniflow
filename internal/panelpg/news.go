package panelpg

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// NewsTranslation is one language of a news post.
type NewsTranslation struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// NewsPost is a news post as the editor sees it.
type NewsPost struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	// Category is the category's code; Kind the notification kind it is
	// announced under, which follows the category.
	Category     string                     `json:"category"`
	Kind         string                     `json:"kind"`
	Tags         []string                   `json:"tags"`
	CoverURL     string                     `json:"coverUrl,omitempty"`
	Featured     bool                       `json:"featured"`
	Views        int64                      `json:"views"`
	Class        string                     `json:"class"`
	Status       string                     `json:"status"`
	ScheduledFor *time.Time                 `json:"scheduledFor,omitempty"`
	PublishedAt  *time.Time                 `json:"publishedAt,omitempty"`
	ExpiresAt    *time.Time                 `json:"expiresAt,omitempty"`
	CreatedAt    time.Time                  `json:"createdAt"`
	UpdatedAt    time.Time                  `json:"updatedAt"`
	TitleEN      string                     `json:"titleEn,omitempty"`
	TitleRU      string                     `json:"titleRu,omitempty"`
	Translations map[string]NewsTranslation `json:"translations,omitempty"`
	Reads        int64                      `json:"reads"`
}

// NewsInput is what the editor saves.
type NewsInput struct {
	Slug     string `json:"slug"`
	Category string `json:"category"`
	Class    string `json:"class"`
	// Tags are up to ten words of letters, digits, and '_'; CoverURL an
	// https image shown above the post; Featured pins it to the top.
	Tags         []string                   `json:"tags"`
	CoverURL     string                     `json:"coverUrl"`
	Featured     bool                       `json:"featured"`
	ExpiresAt    *time.Time                 `json:"expiresAt"`
	Translations map[string]NewsTranslation `json:"translations"`
}

// ErrNewsSlugTaken refuses a slug another post already has.
var ErrNewsSlugTaken = fmt.Errorf("%w: another news post has that slug", ErrConflict)

var (
	newsSlug   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{2,63}$`)
	newsStates = map[string]bool{"draft": true, "scheduled": true, "published": true, "unpublished": true, "archived": true}
)

// NewsPosts lists posts, newest first, optionally of one status.
func (service *Service) NewsPosts(ctx context.Context, status string) ([]NewsPost, error) {
	rows, err := service.queries().SearchNewsPosts(ctx, dbgen.SearchNewsPostsParams{
		Status: pgtype.Text{String: status, Valid: status != ""}, PageSize: 200,
	})
	if err != nil {
		return nil, err
	}
	posts := make([]NewsPost, 0, len(rows))
	for _, row := range rows {
		post := newsPostFrom(row.NewsPost)
		post.TitleEN, post.TitleRU = row.TitleEn, row.TitleRu
		posts = append(posts, post)
	}
	return posts, nil
}

// NewsPost reads one post with its translations and read count.
func (service *Service) NewsPost(ctx context.Context, postID string) (NewsPost, error) {
	id, err := parseUUID(postID)
	if err != nil {
		return NewsPost{}, err
	}
	queries := service.queries()
	row, err := queries.GetNewsPostForEdit(ctx, id)
	if err != nil {
		return NewsPost{}, notFound(err)
	}
	post := newsPostFrom(row)
	localizations, err := queries.ListNewsPostLocalizations(ctx, id)
	if err != nil {
		return NewsPost{}, err
	}
	post.Translations = map[string]NewsTranslation{}
	for _, localization := range localizations {
		post.Translations[localization.Locale] = NewsTranslation{Title: localization.Title, Body: localization.Body}
	}
	post.TitleEN, post.TitleRU = post.Translations["en"].Title, post.Translations["ru"].Title
	if post.Reads, err = queries.CountNewsReads(ctx, id); err != nil {
		return NewsPost{}, err
	}
	return post, nil
}

// SaveNewsPost creates a post (empty postID) or edits one. A translation with
// an empty title and body is removed; at least one language must remain,
// because a post with no text is a badge that opens onto nothing.
func (service *Service) SaveNewsPost(ctx context.Context, postID string, input NewsInput, actor Actor) (NewsPost, error) {
	input.Slug = strings.ToLower(strings.TrimSpace(input.Slug))
	if input.Class == "" {
		input.Class = "transactional"
	}
	input.CoverURL = strings.TrimSpace(input.CoverURL)
	tags, err := normalizeNewsTags(input.Tags)
	if err != nil || !newsSlug.MatchString(input.Slug) || input.Category == "" ||
		(input.Class != "transactional" && input.Class != "marketing") ||
		(input.CoverURL != "" && !validNewsCover(input.CoverURL)) {
		return NewsPost{}, ErrValidaton
	}
	var categoryKnown bool
	if err = service.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM news_categories WHERE code = $1)`,
		input.Category).Scan(&categoryKnown); err != nil {
		return NewsPost{}, err
	}
	if !categoryKnown {
		return NewsPost{}, ErrValidaton
	}
	kept := 0
	for locale, translation := range input.Translations {
		if locale != "ru" && locale != "en" {
			return NewsPost{}, ErrValidaton
		}
		title, body := strings.TrimSpace(translation.Title), strings.TrimSpace(translation.Body)
		if title == "" && body == "" {
			continue
		}
		if title == "" || body == "" || utf8.RuneCountInString(title) > 200 || utf8.RuneCountInString(body) > 3500 {
			return NewsPost{}, ErrValidaton
		}
		kept++
	}
	if kept == 0 {
		return NewsPost{}, ErrValidaton
	}
	var id pgtype.UUID
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		expires := pgtype.Timestamptz{}
		if input.ExpiresAt != nil {
			expires = pgtype.Timestamptz{Time: input.ExpiresAt.UTC(), Valid: true}
		}
		action := "news.updated"
		if postID == "" {
			action = "news.created"
			createdBy := pgtype.UUID{}
			if actor.AdminID != "" {
				createdBy, _ = parseUUID(actor.AdminID)
			}
			existing, lookupErr := queries.SearchNewsPosts(ctx, dbgen.SearchNewsPostsParams{PageSize: 1000})
			if lookupErr != nil {
				return lookupErr
			}
			for _, row := range existing {
				if row.NewsPost.Slug == input.Slug {
					return ErrNewsSlugTaken
				}
			}
			created, createErr := queries.UpsertNewsPost(ctx, dbgen.UpsertNewsPostParams{
				Slug: input.Slug, CategoryCode: input.Category, Class: input.Class, CreatedBy: createdBy,
			})
			if createErr != nil {
				return createErr
			}
			id = created.ID
		} else {
			parsed, parseErr := parseUUID(postID)
			if parseErr != nil {
				return parseErr
			}
			id = parsed
		}
		if _, err := queries.UpdateNewsPostMeta(ctx, dbgen.UpdateNewsPostMetaParams{
			PostID: id, Slug: input.Slug, CategoryCode: input.Category, Class: input.Class, ExpiresAt: expires,
			Tags: tags, CoverUrl: pgtype.Text{String: input.CoverURL, Valid: input.CoverURL != ""},
			Featured: input.Featured,
		}); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrNewsSlugTaken
			}
			if errors.As(err, &pgErr) && pgErr.Code == "23514" {
				return ErrValidaton
			}
			return notFound(err)
		}
		for _, locale := range []string{"ru", "en"} {
			translation := input.Translations[locale]
			title, body := strings.TrimSpace(translation.Title), strings.TrimSpace(translation.Body)
			if title == "" && body == "" {
				if err := queries.DeleteNewsPostLocalization(ctx, dbgen.DeleteNewsPostLocalizationParams{PostID: id, Locale: locale}); err != nil {
					return err
				}
				continue
			}
			if err := queries.UpsertNewsPostLocalization(ctx, dbgen.UpsertNewsPostLocalizationParams{
				PostID: id, Locale: locale, Title: title, Body: body,
			}); err != nil {
				return err
			}
		}
		return appendAudit(ctx, queries, actor.audit(action, "marketing", "news_post", uuidString(id),
			map[string]any{"slug": input.Slug, "category": input.Category, "class": input.Class,
				"featured": input.Featured, "tags": tags}))
	})
	if err != nil {
		return NewsPost{}, err
	}
	return service.NewsPost(ctx, uuidString(id))
}

// SetNewsState publishes, schedules, unpublishes, archives, or returns a post
// to draft. A scheduled post is published by the worker's campaign pass.
func (service *Service) SetNewsState(
	ctx context.Context, postID, status string, scheduledFor *time.Time, actor Actor,
) (NewsPost, error) {
	id, err := parseUUID(postID)
	if err != nil {
		return NewsPost{}, err
	}
	if !newsStates[status] || (status == "scheduled") != (scheduledFor != nil) {
		return NewsPost{}, ErrValidaton
	}
	if scheduledFor != nil && !scheduledFor.After(service.now()) {
		return NewsPost{}, ErrValidaton
	}
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		if status == "published" || status == "scheduled" {
			localizations, lookupErr := queries.ListNewsPostLocalizations(ctx, id)
			if lookupErr != nil {
				return lookupErr
			}
			if len(localizations) == 0 {
				return ErrValidaton
			}
		}
		scheduled := pgtype.Timestamptz{}
		if scheduledFor != nil {
			scheduled = pgtype.Timestamptz{Time: scheduledFor.UTC(), Valid: true}
		}
		if _, err := queries.SetNewsPostState(ctx, dbgen.SetNewsPostStateParams{
			PostID: id, Status: status, ScheduledFor: scheduled,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		return appendAudit(ctx, queries, actor.audit("news.state_changed", "marketing", "news_post", postID,
			map[string]any{"status": status}))
	})
	if err != nil {
		return NewsPost{}, err
	}
	return service.NewsPost(ctx, postID)
}

func newsPostFrom(row dbgen.NewsPost) NewsPost {
	return NewsPost{
		ID: uuidString(row.ID), Slug: row.Slug, Category: row.CategoryCode, Kind: row.Category, Class: row.Class,
		Status: row.Status, Tags: tagsOrEmpty(row.Tags), CoverURL: row.CoverUrl.String, Featured: row.Featured,
		Views:        row.ViewCount,
		ScheduledFor: timePointer(row.ScheduledFor), PublishedAt: timePointer(row.PublishedAt),
		ExpiresAt: timePointer(row.ExpiresAt), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func tagsOrEmpty(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}
